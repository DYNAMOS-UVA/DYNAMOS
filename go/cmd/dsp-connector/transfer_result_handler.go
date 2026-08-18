package main

import "net/http"

// transferResultHandler implements GET /transfers/{providerPid}/result - the
// real HttpData-PULL endpoint a genuine external consumer's own dataplane
// fetches from, at the endpoint URL DYNAMOS's own EDR (built in
// transfer-process-service's markStartedThenCompleted) points it at.
//
// DAT-verified and ownership-checked exactly like every other Provider-role
// transfer route (transfer_handler.go) - no bespoke pull token invented for
// this. The real DCP identity that negotiated and requested the transfer is
// the same one expected to pull its own result, so this reuses
// participantFromRequest/fetchTransfer/checkTransferOwnership unchanged.
func transferResultHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	t, err := fetchTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, "", err)
		return
	}
	if err := checkTransferOwnership(t, participant); err != nil {
		mapTransferServiceError(w, providerPid, "", err)
		return
	}
	if len(t.DataAddress) == 0 {
		writeTransferError(w, http.StatusNotFound, providerPid, t.ConsumerPid, "not-found", "No result available for this transfer yet.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(t.DataAddress)
}
