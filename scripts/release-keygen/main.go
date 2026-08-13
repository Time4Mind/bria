package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	privatePath := flag.String("private-key", "", "new private key path")
	flag.Parse()
	if flag.NArg() != 0 || *privatePath == "" {
		fatal(errors.New("usage: release-keygen --private-key PATH"))
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	file, err := os.OpenFile(*privatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(privateKey) + "\n"
	if _, err := file.WriteString(encoded); err != nil {
		_ = file.Close()
		fatal(err)
	}
	if err := file.Close(); err != nil {
		fatal(err)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(publicKey))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release-keygen:", err)
	os.Exit(1)
}
