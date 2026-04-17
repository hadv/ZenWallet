//go:build hsm

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/ThalesGroup/crypto11"
)

type HSMConfig struct {
	Enabled    bool
	Module     string
	TokenLabel string
	PIN        string
	KEKLabel   string
}

type HSMManager struct {
	ctx    *crypto11.Context
	kek    *crypto11.SecretKey
	config HSMConfig
}

func NewHSMManager(cfg HSMConfig) (*HSMManager, error) {
	if !cfg.Enabled {
		return nil, errors.New("HSM is not enabled")
	}

	c11Config := &crypto11.Config{
		Path:       cfg.Module,
		TokenLabel: cfg.TokenLabel,
		Pin:        cfg.PIN,
	}

	ctx, err := crypto11.Configure(c11Config)
	if err != nil {
		return nil, fmt.Errorf("failed to configure HSM: %w", err)
	}

	id := []byte(cfg.KEKLabel)
	
	// Try to find the key
	key, err := ctx.FindKey(id, nil)
	if err != nil {
		ctx.Close()
		return nil, fmt.Errorf("failed to search for KEK: %w", err)
	}

	var kek *crypto11.SecretKey
	var ok bool
	if key != nil {
		kek, ok = key.(*crypto11.SecretKey)
		if !ok {
			ctx.Close()
			return nil, fmt.Errorf("found key but it is not a secret key")
		}
		log.Printf("HSM: Found existing KEK with label '%s'", cfg.KEKLabel)
	} else {
		// Key not found, generate it
		log.Printf("HSM: Generating new KEK with label '%s'", cfg.KEKLabel)
		kek, err = ctx.GenerateSecretKey(id, 256, crypto11.CipherAES)
		if err != nil {
			ctx.Close()
			return nil, fmt.Errorf("failed to generate KEK: %w", err)
		}
	}

	return &HSMManager{
		ctx:    ctx,
		kek:    kek,
		config: cfg,
	}, nil
}

func (m *HSMManager) EncryptKeyshare(data []byte) ([]byte, error) {
	if m.kek == nil {
		return nil, errors.New("KEK not initialized")
	}

	gcm, err := m.kek.NewGCM()
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM cipher: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

func (m *HSMManager) DecryptKeyshare(ciphertext []byte) ([]byte, error) {
	if m.kek == nil {
		return nil, errors.New("KEK not initialized")
	}

	gcm, err := m.kek.NewGCM()
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM cipher: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, encrypted := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt keyshare: %w", err)
	}

	return plaintext, nil
}

type hsmRandReader struct {
	ctx *crypto11.Context
}

func (r *hsmRandReader) Read(p []byte) (n int, err error) {
	b, err := r.ctx.GenerateRandom(len(p))
	if err != nil {
		return 0, err
	}
	copy(p, b)
	return len(p), nil
}

func (m *HSMManager) SecureRandReader() io.Reader {
	return &hsmRandReader{ctx: m.ctx}
}

func (m *HSMManager) Close() {
	if m.ctx != nil {
		m.ctx.Close()
	}
}
