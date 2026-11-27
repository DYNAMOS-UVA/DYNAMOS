// Command authtool makes the keys and tokens for the jwt auth provider
// (pkg/auth/jwtauth).
//
//	authtool keygen -name orchestrator -out ./keys
//	    Writes orchestrator.key.pem (mode 0600) and orchestrator.pub.pem.
//
//	authtool pubkey -key ./keys/orchestrator.key.pem
//	    Writes the public key of a private key to stdout.
//
//	authtool token -key ./keys/orchestrator.key.pem -issuer dynamos-orchestrator \
//	    -sub jorrit.stutterheim@cloudnation.nl -aud UVA -scope agent:sqlDataRequest \
//	    -attr jobId=jorrit-stutterheim-abcd -ttl 5m
//	    Writes a signed token to stdout.
//
// The keys have the same format as "openssl genpkey -algorithm ed25519".
// Use the token command for debugging and operations only: in normal
// operation the services mint their own short-lived tokens.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
)

const usage = `usage: authtool <command> [flags]

commands:
  keygen   make an Ed25519 key pair
  pubkey   write the public key of a private key
  token    mint a signed token

Run "authtool <command> -h" for the flags of a command.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "authtool:", err)
		}
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], stdout, stderr)
	case "pubkey":
		return pubkey(args[1:], stdout, stderr)
	case "token":
		return token(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

var keyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func keygen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "key name: the files are <name>.key.pem and <name>.pub.pem (required)")
	out := fs.String("out", ".", "output directory")
	force := fs.Bool("force", false, "replace existing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !keyName.MatchString(*name) {
		return fmt.Errorf("-name %q: use letters, digits, '.', '_' and '-'", *name)
	}

	key, err := jwtauth.GenerateKey()
	if err != nil {
		return err
	}
	privPEM, err := jwtauth.MarshalPrivateKeyPEM(key)
	if err != nil {
		return err
	}
	pub := key.Public().(ed25519.PublicKey)
	pubPEM, err := jwtauth.MarshalPublicKeyPEM(pub)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	privPath := filepath.Join(*out, *name+".key.pem")
	pubPath := filepath.Join(*out, *name+".pub.pem")
	if !*force {
		for _, p := range []string{privPath, pubPath} {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s exists (use -force to replace it)", p)
			}
		}
	}
	if err := writeFile(privPath, privPEM, 0o600); err != nil {
		return err
	}
	if err := writeFile(pubPath, pubPEM, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n%s\nkid %s\n", privPath, pubPath, jwtauth.KeyID(pub))
	return nil
}

// writeFile writes data with mode perm, also when the file exists with a
// wider mode.
func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("-key is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return jwtauth.ParsePrivateKeyPEM(data)
}

func pubkey(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "", "private key PEM file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	pubPEM, err := jwtauth.MarshalPublicKeyPEM(key.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	_, err = stdout.Write(pubPEM)
	return err
}

// listFlag collects a comma-separated flag that can also repeat.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// attrFlag collects repeated key=value flags.
type attrFlag map[string]string

func (a attrFlag) String() string { return fmt.Sprint(map[string]string(a)) }

func (a attrFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return fmt.Errorf("%q is not key=value", v)
	}
	a[strings.TrimSpace(k)] = val
	return nil
}

func token(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "", "private key PEM file (required)")
	issuer := fs.String("issuer", "", "\"iss\": must match the verifier's ISSUER setting (required)")
	sub := fs.String("sub", "", "\"sub\": the user or service name (required)")
	kind := fs.String("kind", string(auth.KindUser), "subject kind: user, service or participant")
	ttl := fs.Duration("ttl", jwtauth.DefaultTTL, "token lifetime")
	var aud, scopes listFlag
	attrs := attrFlag{}
	fs.Var(&aud, "aud", "\"aud\": recipients, comma-separated or repeated (required)")
	fs.Var(&scopes, "scope", "scopes, comma-separated or repeated")
	fs.Var(attrs, "attr", "extra attribute key=value, repeated (for example jobId=...)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch auth.Kind(*kind) {
	case auth.KindUser, auth.KindService, auth.KindParticipant:
	default:
		return fmt.Errorf("-kind %q: use user, service or participant", *kind)
	}
	if *ttl <= 0 {
		return errors.New("-ttl must be positive")
	}
	key, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	// The TTL flag is the cap: an operator asks for the lifetime explicitly.
	iss, err := jwtauth.NewIssuer(*issuer, key, jwtauth.WithTTL(*ttl, *ttl))
	if err != nil {
		return err
	}
	tok, err := iss.Issue(context.Background(),
		auth.Principal{Subject: *sub, Kind: auth.Kind(*kind)},
		auth.Grant{Audience: aud, Scopes: scopes, TTL: *ttl, Attributes: attrs})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, tok.Value)
	fmt.Fprintf(stderr, "expires %s\n", tok.ExpiresAt.UTC().Format(time.RFC3339))
	return nil
}
