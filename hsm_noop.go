//go:build !hsm

package main

import (
	"crypto/rand"
	"io"
)

type HSMConfig struct {
	Enabled    bool
	Module     string
	TokenLabel string
	PIN        string
	KEKLabel   string
}

type HSMManager struct {
	config HSMConfig
}

func NewHSMManager(cfg HSMConfig) (*HSMManager, error) {
	return &HSMManager{config: cfg}, nil
}

func (m *HSMManager) EncryptKeyshare(data []byte) ([]byte, error) {
	return data, nil
}

func (m *HSMManager) DecryptKeyshare(ciphertext []byte) ([]byte, error) {
	return ciphertext, nil
}

func (m *HSMManager) SecureRandReader() io.Reader {
	return rand.Reader
}

func (m *HSMManager) Close() {
	// No-op
}
