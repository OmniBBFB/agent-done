package remote

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"

	"github.com/cisco/go-hpke"
)

const (
	protocolInfo = "agent-done/control/v1"
	maxPayload   = 256 << 10
	keyLength    = 32
	tagLength    = 16
)

// NewKeys creates raw X25519 keys for this endpoint. The private key belongs
// in the local device config. Only the public key is exchanged.
func NewKeys() (private, public []byte, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return key.Bytes(), key.PublicKey().Bytes(), nil
}

type cipher struct {
	suite hpke.CipherSuite
	key   hpke.KEMPrivateKey
	peers map[string]hpke.KEMPublicKey
	id    string
}

func newCipher(id string, private []byte, peers map[string][]byte) (*cipher, error) {
	suite, err := hpke.AssembleCipherSuite(hpke.DHKEM_X25519, hpke.KDF_HKDF_SHA256, hpke.AEAD_CHACHA20POLY1305)
	if err != nil {
		return nil, err
	}
	key, err := suite.KEM.DeserializePrivateKey(private)
	if err != nil {
		return nil, errors.New("invalid endpoint private key")
	}
	publicKeys := make(map[string]hpke.KEMPublicKey, len(peers))
	for peer, raw := range peers {
		public, err := suite.KEM.DeserializePublicKey(raw)
		if err != nil {
			return nil, errors.New("invalid paired public key")
		}
		publicKeys[peer] = public
	}
	return &cipher{suite: suite, key: key, peers: publicKeys, id: id}, nil
}

func routeAAD(from, to string) []byte {
	return []byte(protocolInfo + "\n" + from + "\n" + to)
}

func (c *cipher) seal(to string, data []byte) ([]byte, error) {
	public, ok := c.peers[to]
	if !ok || len(data) > maxPayload-keyLength-tagLength {
		return nil, errors.New("unknown destination or oversized message")
	}
	enc, sender, err := hpke.SetupAuthS(c.suite, rand.Reader, public, c.key, []byte(protocolInfo))
	if err != nil {
		return nil, errors.New("could not seal message")
	}
	return append(enc, sender.Seal(routeAAD(c.id, to), data)...), nil
}

func (c *cipher) open(from string, packet []byte) ([]byte, error) {
	public, ok := c.peers[from]
	if !ok || len(packet) < keyLength+tagLength || len(packet) > maxPayload {
		return nil, errors.New("unknown sender or invalid ciphertext length")
	}
	receiver, err := hpke.SetupAuthR(c.suite, c.key, public, packet[:keyLength], []byte(protocolInfo))
	if err != nil {
		return nil, errors.New("message authentication failed")
	}
	clear, err := receiver.Open(routeAAD(from, c.id), packet[keyLength:])
	if err != nil {
		return nil, errors.New("message authentication failed")
	}
	return clear, nil
}
