package modem

import (
	"crypto/sha1"
	"hash"
	"time"
)

func sha1New() hash.Hash { return sha1.New() }
func testSleep()         { time.Sleep(5 * time.Millisecond) }
