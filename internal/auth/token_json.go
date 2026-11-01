package auth

import "encoding/json"

type tokenAlias APIToken

func marshalToken(t APIToken) ([]byte, error) {
	return json.Marshal(struct {
		tokenAlias
		Hash string `json:"hash,omitempty"`
	}{tokenAlias: tokenAlias(t), Hash: t.Hash})
}
func unmarshalToken(b []byte, t *APIToken) error {
	var v struct {
		tokenAlias
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*t = APIToken(v.tokenAlias)
	t.Hash = v.Hash
	return nil
}
