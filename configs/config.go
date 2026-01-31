package configs

type RedisConfig struct {
	REDIS_URI      string
	REDIS_PORT     string
	REDIS_HOST     string
	REDIS_USER     string
	REDIS_PASSWORD string
	REDIS_PREFIX   string
}

type LatticeConfig struct {
}

type Config struct {
	RedisConfig
}

func Load() (*Config, error) {
	redisConf := RedisConfig{
		REDIS_URI:      GetEnv("REDIS_URI", ""),
		REDIS_PORT:     GetEnv("REDIS_PORT", "2567"),
		REDIS_HOST:     GetEnv("REDIS_HOST", "localhost"),
		REDIS_USER:     GetEnv("REDIS_USER", "root"),
		REDIS_PASSWORD: GetEnv("REDIS_PASSWORD", "password"),
		REDIS_PREFIX:   GetEnv("REDIS_PREFIX", "lattice:"),
	}
	return &Config{
		RedisConfig: redisConf,
	}, nil
}
