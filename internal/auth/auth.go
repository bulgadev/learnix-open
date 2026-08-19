// Package auth handles password hashing and input validation.
package auth

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of pw.
func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword compares a bcrypt hash against a plaintext pw.
func VerifyPassword(hash, pw string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw))
}

// dummyHash is a fixed bcrypt hash used to equalize login timing for unknown
// emails (so response time does not reveal whether an account exists).
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

// BlindVerify runs a bcrypt comparison that always fails, spending the same
// time a real verification would. Used when the account does not exist.
func BlindVerify(pw string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(pw))
}

// ErrShortPassword / ErrLongPassword / ErrInvalidEmail are returned by the validators.
var (
	ErrShortPassword = errors.New("senha precisa ter ao menos 8 caracteres")
	ErrLongPassword  = errors.New("senha excede 72 caracteres (limite do bcrypt)")
	ErrInvalidEmail  = errors.New("email invalido")
)

// ValidatePassword enforces the bcrypt length window (8..72).
func ValidatePassword(pw string) error {
	n := len(pw)
	if n < 8 {
		return ErrShortPassword
	}
	if n > 72 {
		return ErrLongPassword
	}
	return nil
}

// ValidateEmail does a minimal, dependency-free email check: non-empty,
// <= 254 chars, exactly one '@', with non-empty local and domain parts.
func ValidateEmail(s string) error {
	e := strings.TrimSpace(s)
	if len(e) == 0 || len(e) > 254 {
		return ErrInvalidEmail
	}
	at := strings.IndexByte(e, '@')
	if at <= 0 {
		return ErrInvalidEmail
	}
	rest := e[at+1:]
	if len(rest) == 0 || strings.IndexByte(rest, '@') != -1 || strings.LastIndexByte(rest, '.') == 0 {
		return ErrInvalidEmail
	}
	return nil
}
