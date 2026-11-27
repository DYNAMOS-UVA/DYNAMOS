package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOwnAuthorization(t *testing.T) {
	defer func(dat, u, id, sec string) { partyDAT, stsTokenURL, stsClientID, stsClientSecret = dat, u, id, sec }(partyDAT, stsTokenURL, stsClientID, stsClientSecret)
	stsTokenURL, stsClientID, stsClientSecret = "", "", ""

	partyDAT = ""
	assert.Equal(t, "", ownAuthorization("did:web:p"), "no credential: no header")

	partyDAT = "dat"
	assert.Equal(t, "Bearer dat", ownAuthorization("did:web:p"))

	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		assert.Equal(t, "did:web:p", r.Form.Get("audience"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"sts-token"}`))
	}))
	defer sts.Close()
	stsTokenURL, stsClientID, stsClientSecret = sts.URL, "id", "secret"
	assert.Equal(t, "Bearer sts-token", ownAuthorization("did:web:p"), "STS comes first")

	stsTokenURL = "http://127.0.0.1:1/unreachable"
	assert.Equal(t, "Bearer dat", ownAuthorization("did:web:p"), "STS failure falls back to PARTY_DAT")
}

func TestLooksLikeCredential(t *testing.T) {
	for _, v := range []string{"Bearer abc", "bearer abc", "  Bearer x", "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0.sig"} {
		assert.True(t, looksLikeCredential(v), v)
	}
	for _, v := range []string{"did:web:localhost%3A9999", "did:web:vu.example.com", "urn:dynamos:party:VU", "jorrit.stutterheim@cloudnation.nl"} {
		assert.False(t, looksLikeCredential(v), v)
	}
}

// TestTransfersConsumerCollectionHandler_RefusesTokenAsParticipant
// checks that a token never gets stored as the participant. The handler
// answers before it sends anything or touches the store.
func TestTransfersConsumerCollectionHandler_RefusesTokenAsParticipant(t *testing.T) {
	body := `{"negotiationId":"n","participant":"Bearer eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0.sig","format":"f","callbackAddress":"http://c"}`
	rec := httptest.NewRecorder()
	transfersConsumerCollectionHandler(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/transfers/consumer", strings.NewReader(body)))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid-participant")
}
