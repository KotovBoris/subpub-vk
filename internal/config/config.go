package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Все конфигурационные параметры приложения.
type Config struct {
	GRPCServer GRPCServerConfig `yaml:"grpc_server"`
	Logger     LoggerConfig     `yaml:"logger"`
}

type GRPCServerConfig struct {
	Port            string        `yaml:"port"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type LoggerConfig struct {
	Level string `yaml:"level"`
}

// MustLoad загружает конфигурацию из файла.
// Принимает путь к файлу конфигурации. Паникует при ошибке.
func MustLoad(configPath string) *Config {
	if configPath == "" {
		configPath = os.Getenv("CONFIG_PATH")
		if configPath == "" {
			configPath = "./configs/config.yaml" // Путь по умолчанию
		}
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		panic("cannot read config file: " + err.Error())
	}

	var cfg Config

	if err := yaml.Unmarshal(configBytes, &cfg); err != nil {
		panic("cannot parse config file: " + err.Error())
	}

	// Валидация
	if cfg.GRPCServer.Port == "" {
		panic("grpc server port is not defined in config")
	}
	if cfg.Logger.Level == "" {
		panic("logger level is not defined in config")
	}
	if cfg.GRPCServer.ShutdownTimeout <= 0 { // Проверяем, что таймаут > 0
		panic("grpc server shutdown_timeout must be defined and positive in config")
	}

	return &cfg
}
