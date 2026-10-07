// Package auth holds the panel's own credentials: the admin password hash,
// login sessions, login rate limiting and the Cloudflare Turnstile check.
package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is above the library default to make offline cracking of a
// leaked config file more expensive; the panel hashes on login only, so the
// extra ~50ms is invisible.
const bcryptCost = 12

// HashPassword returns a bcrypt hash suitable for config storage.
func HashPassword(pw string) (string, error) {
	if pw == "" {
		return "", errors.New("密码不能为空")
	}
	if len(pw) > 72 {
		return "", errors.New("密码过长（bcrypt 上限 72 字节）")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// MustHashPassword is HashPassword for bootstrap paths where failure is fatal.
func MustHashPassword(pw string) string {
	h, err := HashPassword(pw)
	if err != nil {
		panic(fmt.Sprintf("hash password: %v", err))
	}
	return h
}

// VerifyPassword reports whether pw matches the stored hash.
func VerifyPassword(hash, pw string) bool {
	if hash == "" || pw == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
