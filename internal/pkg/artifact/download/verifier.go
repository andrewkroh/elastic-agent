// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package download

import (
	"bufio"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/openpgp" //nolint:staticcheck // crypto/openpgp is only receiving security updates.

	"github.com/elastic/elastic-agent/internal/pkg/agent/errors"
	"github.com/elastic/elastic-agent/internal/pkg/agent/program"
)

type ChecksumMismatchError struct {
	Expected string
	Computed string
	File     string
}

func (e *ChecksumMismatchError) Error() string {
	return "checksum mismatch for " + e.File + ": expected " + e.Expected + ", computed " + e.Computed
}

type InvalidSignatureError struct {
	File string
	Err  error
}

func (e *InvalidSignatureError) Error() string {
	return "invalid signature for " + e.File + ": " + e.Err.Error()
}

func (e *InvalidSignatureError) Unwrap() error { return e.Err }

// Verifier is an interface verifying the SHA512 checksum and GPG signature and
// of a downloaded artifact.
type Verifier interface {
	// Verify should verify the artifact and return an error if any checks fail.
	Verify(spec program.Spec, version string) error
}

// VerifySHA512Hash checks that a sidecar file containing a sha512 checksum
// exists and that the checksum in the sidecar file matches the checksum of
// the file. It returns an error if validation fails.
func VerifySHA512Hash(filename string) error {
	// Read expected checksum.
	expectedHash, err := readChecksumFile(filename+".sha512", filepath.Base(filename))
	if err != nil {
		return err
	}

	// Compute sha512 checksum.
	f, err := os.Open(filename)
	if err != nil {
		return errors.New(err, errors.TypeFilesystem, errors.M(errors.MetaKeyPath, filename))
	}
	defer f.Close()

	hash := sha512.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	computedHash := hex.EncodeToString(hash.Sum(nil))

	if computedHash != expectedHash {
		return &ChecksumMismatchError{
			Expected: expectedHash,
			Computed: computedHash,
			File:     filename,
		}
	}

	return nil
}

// readChecksumFile reads the checksum of the file named in filename from
// checksumFile. checksumFile is expected to contain the output from the
// shasum family of tools (e.g. sha512sum).
func readChecksumFile(checksumFile, filename string) (string, error) {
	f, err := os.Open(checksumFile)
	if err != nil {
		return "", fmt.Errorf("failed to open checksum file %q: %w", checksumFile, err)
	}
	defer f.Close()

	// The format is a checksum, a space, a character indicating input mode ('*'
	// for binary, ' ' for text or where binary is insignificant), and name for
	// each FILE. See man sha512sum.
	//
	// {hash} SPACE (ASTERISK|SPACE) [{directory} SLASH] {filename}
	var checksum string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 2 {
			// Ignore malformed.
			continue
		}

		lineFilename := strings.TrimLeft(parts[1], "*")
		if lineFilename != filename {
			// Continue looking for a match.
			continue
		}

		checksum = parts[0]
	}

	if len(checksum) == 0 {
		return "", fmt.Errorf("checksum for %q was not found in %q", filename, checksumFile)
	}

	return checksum, nil
}

func VerifyGPGSignature(file string, asciiArmorSignature, publicKey []byte) error {
	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(publicKey))
	if err != nil {
		return errors.New(err, "read armored key ring", errors.TypeSecurity)
	}

	f, err := os.Open(file)
	if err != nil {
		return errors.New(err, errors.TypeFilesystem, errors.M(errors.MetaKeyPath, file))
	}
	defer f.Close()

	_, err = openpgp.CheckArmoredDetachedSignature(keyring, f, bytes.NewReader(asciiArmorSignature))
	if err != nil {
		return &InvalidSignatureError{File: file, Err: err}
	}

	return nil
}
