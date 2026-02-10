package utils

import (
	"crypto/rand"
	"encoding/hex"
)

func RandomUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
