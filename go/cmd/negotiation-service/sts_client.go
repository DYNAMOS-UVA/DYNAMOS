package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// fetchSTSToken mints a fresh, audience-bound DCP self-issued token from a
// real IdentityHub STS endpoint (OAuth2 client_credentials grant), used in
// place of the static partyDAT when stsTokenURL/stsClientID/stsClientSecret
// are configured (see their own doc comments in config_prod.go). A real DCP
// verifier checks the token's "aud" claim against itself, so the token
// cannot be cached across different recipients - callers request one per
// outbound delivery, addressed to that delivery's own recipient.
func fetchSTSToken(audience string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", stsClientID)
	form.Set("client_secret", stsClientSecret)
	form.Set("audience", audience)
	if stsScope != "" {
		form.Set("bearer_access_scope", stsScope)
	}

	req, err := http.NewRequest(http.MethodPost, stsTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building STS token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling STS token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading STS token response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("STS token endpoint returned status %d: %s", resp.StatusCode, body)
	}

	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decoding STS token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("STS token response has no access_token")
	}
	return tr.AccessToken, nil
}
