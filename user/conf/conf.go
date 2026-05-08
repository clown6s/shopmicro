package conf

import "shopmicro/pkg/conf"

type MySQL struct {
	DSN string `yaml:"dsn"`
}

type Redis struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type JWT struct {
	Secret     string `yaml:"secret"`
	Expiration int    `yaml:"expiration"` // 小时
}

type Config struct {
	Server conf.Server `yaml:"server"`
	Etcd   conf.Etcd   `yaml:"etcd"`
	MySQL  MySQL       `yaml:"mysql"`
	Redis  Redis       `yaml:"redis"`
	JWT    JWT         `yaml:"jwt"`
	Mode   string      `yaml:"mode"`
}
