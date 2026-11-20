package remote

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestOpensProtocolFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../protocol/fixtures/hpke-auth-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Vectors []struct {
			From               string `json:"from"`
			To                 string `json:"to"`
			SenderPublicKey    []byte `json:"sender_public_key"`
			ReceiverPrivateKey []byte `json:"receiver_private_key"`
			Payload            []byte `json:"payload"`
			Plaintext          []byte `json:"plaintext"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || len(data.Vectors) != 2 {
		t.Fatalf("invalid vectors: %v", err)
	}
	for _, v := range data.Vectors {
		c, err := newCipher(v.To, v.ReceiverPrivateKey, map[string][]byte{v.From: v.SenderPublicKey})
		if err != nil {
			t.Fatal(err)
		}
		clear, err := c.open(v.From, v.Payload)
		if err != nil || !bytes.Equal(clear, v.Plaintext) {
			t.Fatalf("fixture did not decrypt on route %s: %v", v.From, err)
		}
		c.id = "wrong-destination"
		if _, err := c.open(v.From, v.Payload); err == nil {
			t.Fatal("different route was accepted")
		}
		c.id = v.To
		v.Payload[len(v.Payload)-1] ^= 1
		if _, err := c.open(v.From, v.Payload); err == nil {
			t.Fatal("modified payload was accepted")
		}
	}
}
