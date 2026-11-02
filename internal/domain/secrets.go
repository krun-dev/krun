package domain

type EncryptedWorkerSecret struct {
	Version    WorkerSecretVersion
	Ciphertext []byte
	Nonce      []byte
}
