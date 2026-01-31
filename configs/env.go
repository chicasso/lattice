package configs

import "os"

func GetEnv(tag, defaultVal string) string {
	if val, ok := os.LookupEnv(tag); ok {
		return val
	}
	return defaultVal
}
