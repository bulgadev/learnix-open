package auth

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	h, err := HashPassword("hunter2!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == "hunter2!" {
		t.Fatal("hash equals plaintext")
	}
	if err := VerifyPassword(h, "hunter2!"); err != nil {
		t.Fatalf("verify correct: %v", err)
	}
}

func TestVerifyPassword_Wrong(t *testing.T) {
	h, _ := HashPassword("correct-horse")
	if err := VerifyPassword(h, "wrong-horse"); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestHashPassword_Short(t *testing.T) {
	if _, err := HashPassword("abc"); !errors.Is(err, ErrShortPassword) {
		t.Fatalf("expected ErrShortPassword, got %v", err)
	}
}

func TestHashPassword_TooLong(t *testing.T) {
	pw := make([]byte, 73)
	for i := range pw {
		pw[i] = 'x'
	}
	if _, err := HashPassword(string(pw)); !errors.Is(err, ErrLongPassword) {
		t.Fatalf("expected ErrLongPassword, got %v", err)
	}
}

func TestValidateEmail(t *testing.T) {
	cases := map[string]bool{
		"a@b.co":           true,
		"user@example.com": true,
		"":                 false,
		"no-at":            false,
		"@nodomain.com":    false,
		"user@":            false,
		"user@@double.com": false,
		"user@domain":      true,
		"a@b.c":            true,
	}
	for email, ok := range cases {
		err := ValidateEmail(email)
		if ok && err != nil {
			t.Errorf("ValidateEmail(%q) unexpected error: %v", email, err)
		}
		if !ok && !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("ValidateEmail(%q) expected ErrInvalidEmail, got %v", email, err)
		}
	}
}
