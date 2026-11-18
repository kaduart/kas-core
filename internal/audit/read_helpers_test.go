package audit

import "os"

func osReadFile(p string) ([]byte, error)  { return os.ReadFile(p) }
func osWriteFile(p string, d []byte) error { return os.WriteFile(p, d, 0o600) }
