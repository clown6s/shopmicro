package conf

import "shopmicro/pkg/conf"

type Redis struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type JWT struct {
	Secret string `yaml:"secret"`
}

type Config struct {
	Server conf.Server `yaml:"server"`
	Etcd   conf.Etcd   `yaml:"etcd"`
	Redis  Redis       `yaml:"redis"`
	JWT    JWT         `yaml:"jwt"`
	Mode   string      `yaml:"mode"`
}
