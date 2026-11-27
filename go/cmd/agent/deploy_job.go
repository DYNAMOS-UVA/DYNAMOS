package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/mschain"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
	"go.opencensus.io/trace"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func generateChainAndDeploy(ctx context.Context, compositionRequest *pb.CompositionRequest, localJobName string, options map[string]bool) (context.Context, *batchv1.Job, error) {
	logger.Debug("Starting generateChainAndDeploy")

	ctx, span := trace.StartSpan(ctx, serviceName+"/func: generateChainAndDeploy")
	defer span.End()

	msChain, err := generateMicroserviceChain(compositionRequest, options)
	if err != nil {
		logger.Sugar().Errorf("Error generating microservice chain %v", err)
		return ctx, nil, err
	}
	logger.Sugar().Debug(msChain)
	createdJob, err := deployJob(ctx, msChain, localJobName, compositionRequest)
	if err != nil {
		logger.Sugar().Errorf("Error generating microservice chain %v", err)
		return ctx, nil, err
	}

	return ctx, createdJob, nil
}

func deployJob(ctx context.Context, msChain []mschain.MicroserviceMetadata, jobName string, compositionRequest *pb.CompositionRequest) (*batchv1.Job, error) {
	logger.Debug("Starting deployJob")

	dataStewardName := strings.ToLower(serviceName)
	if dataStewardName == "" {
		return nil, fmt.Errorf("env variable DATA_STEWARD_NAME not defined")
	}

	jobMutex.Lock()
	jobCounter[jobName]++
	newValue := jobCounter[jobName]
	jobMutex.Unlock()

	newJobName := replaceLastCharacter(jobName, newValue)
	// Define the job
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newJobName,
			Namespace: dataStewardName,
			Labels:    jobLabels(dataStewardName, jobName),
		},
		Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds:   &activeDeadlineSeconds,
			TTLSecondsAfterFinished: &ttl, // Clean up job TTL after it finishes
			BackoffLimit:            &backoffLimit,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: jobLabels(dataStewardName, jobName),
				},
				Spec: v1.PodSpec{
					Containers:    []v1.Container{},
					RestartPolicy: v1.RestartPolicyOnFailure,
				},
			},
		},
	}

	// Add the containers to the job
	port := firstPortMicroservice
	nrOfServices := len(msChain)
	firstService := "1"
	lastService := "0"
	// Determine the amount of data providers, only a 'computeProvider' should be able to access this data
	// If it exists, set it as an environment variable below
	nr_of_data_providers := 0
	if compositionRequest.DataProviders != nil {
		nr_of_data_providers = len(compositionRequest.DataProviders)
	}

	for i, microservice := range msChain {
		port++

		if i == nrOfServices-1 {
			lastService = "1"
		}

		logger.Sugar().Debugw("job info:", "name: ", microservice.Name, "Port: ", port)

		microserviceTag := getMicroserviceTag(microservice.Name)

		repositoryName := os.Getenv("MICROSERVICE_REPOSITORY_NAME")
		if repositoryName == "" {
			repositoryName = "dynamos1"
		}

		fullImage := fmt.Sprintf("%s/%s:%s", repositoryName, microservice.Name, microserviceTag)
		logger.Sugar().Debugf("FullImage name: %s", fullImage)
		logger.Sugar().Debugf("Job name: %s", jobName)

		container := v1.Container{
			Name:            microservice.Name,
			Image:           fullImage,
			ImagePullPolicy: "Always",
			Env: []v1.EnvVar{
				{Name: "DATA_STEWARD_NAME", Value: strings.ToUpper(dataStewardName)},
				{Name: "DESIGNATED_GRPC_PORT", Value: strconv.Itoa(port)},
				{Name: "FIRST", Value: firstService},
				{Name: "LAST", Value: lastService},
				{Name: "JOB_NAME", Value: jobName},
				{Name: "SIDECAR_PORT", Value: strconv.Itoa(firstPortMicroservice - 1)},
				{Name: "OC_AGENT_HOST", Value: tracingHost},
				{Name: "NR_OF_DATA_PROVIDERS", Value: strconv.Itoa(nr_of_data_providers)},
				// Add the role to allow further processing based on the role of the agent containing this microservice, such as dataProvider or computeProvider
				{Name: "AGENT_ROLE", Value: compositionRequest.Role},
			},
			// Add additional container configuration here as needed
		}

		job.Spec.Template.Spec.Containers = append(job.Spec.Template.Spec.Containers, container)
		firstService = "0"
	}

	job.Spec.Template.Spec.Containers = append(job.Spec.Template.Spec.Containers, addSidecar())

	if clientSet == nil {
		clientSet = getKubeClient()
	}

	// Create the job
	createdJob, err := clientSet.BatchV1().Jobs(dataStewardName).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		logger.Sugar().Errorf("failed to create job: %v", err)
		return nil, err
	}

	return createdJob, nil
}

// jobLabels are the labels of a job and its pods. They must not include
// "app": <steward>, the selector of the agent's Service. With that label a
// running job pod became an endpoint of the agent Service, and api-gateway
// requests to the agent went to the job pod (502 Bad Gateway).
func jobLabels(dataStewardName, jobName string) map[string]string {
	return map[string]string{"jobOf": dataStewardName, "jobName": jobName}
}

// rabbitSecretName is the Secret with the RabbitMQ passwords of this
// namespace, one key per user (scripts/auth-secrets.sh makes it).
const rabbitSecretName = "dynamos-rabbitmq"

// jobRabbitUser is the RabbitMQ user of the job sidecars this agent starts.
// It is separate from the agent's own user: a job can only consume its
// own job queues and send results back to this agent.
func jobRabbitUser() string {
	return "job-" + strings.ToLower(serviceName)
}

func addSidecar() v1.Container {
	sidecarName := os.Getenv("SIDECAR_NAME")

	if sidecarName == "" {
		sidecarName = "sidecar"
	}

	repositoryName := os.Getenv("SIDECAR_REPOSITORY_NAME")
	if repositoryName == "" {
		repositoryName = "dynamos1"
	}

	sidecarTag := getMicroserviceTag(sidecarName)

	fullImage := fmt.Sprintf("%s/%s:%s", repositoryName, sidecarName, sidecarTag)
	logger.Sugar().Debugf("Sidecar image name: %s", fullImage)

	return v1.Container{
		Name:            sidecarName,
		Image:           fullImage,
		ImagePullPolicy: "Always",
		Env: []v1.EnvVar{
			{Name: "DESIGNATED_GRPC_PORT", Value: strconv.Itoa(firstPortMicroservice - 1)},
			{Name: "TEMPORARY_JOB", Value: "true"},
			{Name: "AMQ_USER", Value: jobRabbitUser()},
			{Name: "OC_AGENT_HOST", Value: tracingHost},
			{Name: "AMQ_PASSWORD",
				ValueFrom: &v1.EnvVarSource{
					SecretKeyRef: &v1.SecretKeySelector{
						LocalObjectReference: v1.LocalObjectReference{
							Name: rabbitSecretName,
						},
						Key: jobRabbitUser(),
					},
				},
			}},
		// Add additional container configuration here as needed
	}
}

func getRequiredMicroservices(microserviceMetada *[]mschain.MicroserviceMetadata, request *mschain.RequestType, role string) error {
	logger.Sugar().Debug("started getRequiredMicroservices")
	for _, ms := range request.RequiredServices {
		var metadataObject mschain.MicroserviceMetadata

		_, err := etcd.GetAndUnmarshalJSON(etcdClient, fmt.Sprintf("/microservices/%s/chainMetadata", ms), &metadataObject)
		if err != nil {
			return err
		}

		if strings.EqualFold(metadataObject.Label, role) {
			*microserviceMetada = append(*microserviceMetada, metadataObject)
		} else if strings.EqualFold("all", role) {
			// Only append dataProvider microservices
			*microserviceMetada = append(*microserviceMetada, metadataObject)
		}
	}

	return nil
}

func getOptionalMicroservices(microserviceMetada *[]mschain.MicroserviceMetadata, request *mschain.RequestType, role string, requestType string, options map[string]bool) error {
	//TODO: include enforced microservices
	logger.Debug("Start getOptionalMicroservices")
	logger.Sugar().Debug(len(request.OptionalServices))

	// Parse options for optional microservices
	for option, boolVal := range options {
		logger.Sugar().Debugf("Option: %s boolVal: %b", option, boolVal)

		if boolVal {
			// Possibly add microservice to the list
			for msName, optionKey := range request.OptionalServices {
				if strings.EqualFold(option, optionKey) {
					var metadataObject mschain.MicroserviceMetadata

					_, err := etcd.GetAndUnmarshalJSON(etcdClient, fmt.Sprintf("/microservices/%s/chainMetadata", msName), &metadataObject)
					if err != nil {
						return err
					}

					if strings.EqualFold(metadataObject.Label, role) {
						*microserviceMetada = append(*microserviceMetada, metadataObject)
					} else if strings.EqualFold("all", role) {
						// Only append dataProvider microservices
						*microserviceMetada = append(*microserviceMetada, metadataObject)
					}
				}
			}
		}
	}

	return nil
}
func RequestTypeMicroservices(requestType string) (mschain.RequestType, error) {
	var request mschain.RequestType
	_, err := etcd.GetAndUnmarshalJSON(etcdClient, fmt.Sprintf("/requestTypes/%s", requestType), &request)
	if err != nil {
		return mschain.RequestType{}, err
	}

	return request, nil
}

func replaceLastCharacter(name string, replaceWith int) string {
	if name == "" {
		return ""
	}

	nameSlice := []rune(name)
	nameSlice = nameSlice[:len(nameSlice)-1]

	runeSlice := []rune(strconv.Itoa(replaceWith))
	nameSlice = append(nameSlice, runeSlice...)

	return string(nameSlice)
}

func getMicroserviceTag(msName string) string {
	tag := os.Getenv(fmt.Sprintf("%s_TAG", strings.ToUpper(msName)))
	logger.Sugar().Debugf("Tag in getMicroserviceTag: %s", tag)

	if tag != "" {
		return tag
	}

	return "main"
}
