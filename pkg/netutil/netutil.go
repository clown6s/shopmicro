package netutil

import "net"

// GetLocalIP 通过 UDP 拨号获取本机对外通信的 IP 地址。
// 操作系统会自动选择有默认路由的网卡，避免选到虚拟网卡或 link-local 地址。
func GetLocalIP() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP, nil
}
