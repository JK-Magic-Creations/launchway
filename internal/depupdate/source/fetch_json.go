package source

import "encoding/json"

func tokenFrom(b []byte) (string, error) {
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return "", err
	}
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}
