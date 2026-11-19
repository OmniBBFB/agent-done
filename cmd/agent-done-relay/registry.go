package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type DeviceConfig struct {
	ID          string   `json:"id"`
	Role        string   `json:"role"`
	TokenSHA256 string   `json:"token_sha256"`
	Allow       []string `json:"allow"`
}

type Config struct {
	Devices []DeviceConfig `json:"devices"`
}

type device struct {
	id     string
	role   string
	digest [sha256.Size]byte
	allow  map[string]bool
}

type registry struct {
	byID    map[string]device
	byToken map[[sha256.Size]byte]device
}

func decode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func readRegistry(path string) (registry, error) {
	file, err := os.Open(path)
	if err != nil {
		return registry{}, err
	}
	defer file.Close()
	const maxConfigBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return registry{}, err
	}
	if len(data) > maxConfigBytes {
		return registry{}, fmt.Errorf("device registry exceeds %d bytes", maxConfigBytes)
	}
	var config Config
	if err := decode(data, &config); err != nil {
		return registry{}, fmt.Errorf("invalid device registry: %w", err)
	}
	return newRegistry(config)
}

func newRegistry(config Config) (registry, error) {
	r := registry{byID: make(map[string]device), byToken: make(map[[sha256.Size]byte]device)}
	for _, entry := range config.Devices {
		if !identifier.MatchString(entry.ID) || (entry.Role != "phone" && entry.Role != "workstation") {
			return registry{}, fmt.Errorf("invalid device ID or role")
		}
		if _, ok := r.byID[entry.ID]; ok {
			return registry{}, fmt.Errorf("duplicate device ID %s", entry.ID)
		}
		d := device{id: entry.ID, role: entry.Role, allow: make(map[string]bool)}
		n, err := hex.Decode(d.digest[:], []byte(entry.TokenSHA256))
		if len(entry.TokenSHA256) != hex.EncodedLen(sha256.Size) || err != nil || n != sha256.Size {
			return registry{}, fmt.Errorf("invalid credential digest for %s", entry.ID)
		}
		if _, ok := r.byToken[d.digest]; ok {
			return registry{}, fmt.Errorf("device %s reuses a credential", entry.ID)
		}
		for _, target := range entry.Allow {
			if !identifier.MatchString(target) || target == entry.ID {
				return registry{}, fmt.Errorf("invalid route from %s", entry.ID)
			}
			d.allow[target] = true
		}
		r.byID[d.id], r.byToken[d.digest] = d, d
	}
	return r, nil
}

func newCredential() (token string, digest string) {
	var randomness [32]byte
	rand.Read(randomness[:])
	token = base64.RawURLEncoding.EncodeToString(randomness[:])
	hash := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(hash[:])
}
