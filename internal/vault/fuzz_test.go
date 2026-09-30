package vault

import "testing"

// FuzzParse feeds arbitrary bytes to the vault file parser: it must never
// panic, and anything it accepts must have in-bounds parameters.
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"format":"tunneltab-vault","version":1,"kdf":{"name":"argon2id","time":1,"memoryKiB":8192,"threads":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="},"cipher":"aes-256-gcm","nonce":"AAAAAAAAAAAAAAAA","ciphertext":"AA=="}`))
	f.Add([]byte(`{"format":"tunneltab-vault","version":1,"kdf":{"memoryKiB":1073741824}}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := parse(data)
		if err != nil {
			return
		}
		if file.header.validate() != nil {
			t.Fatal("parse accepted a header that fails validation")
		}
		p := file.header.KDF.Params
		if p.MemoryKiB > maxMemoryKiB || p.Time > maxTime || p.Threads > maxThreads {
			t.Fatalf("accepted out-of-bounds parameters %+v", p)
		}
	})
}
