package discover

import "strings"

// app is a well-known program: its usual name and which of its ports are
// web pages (by the port inside the container, or on the server for
// port-only matches).
type app struct {
	name   string
	ports  map[int]endpoint
	notWeb bool // a database or similar: never a web page
}

type endpoint struct {
	protocol string // "http" or "https"
	path     string // opened after the address, e.g. "/admin"
}

var (
	httpEP  = endpoint{protocol: "http"}
	httpsEP = endpoint{protocol: "https"}
)

func web(ports map[int]endpoint, name string) *app { return &app{name: name, ports: ports} }
func notWeb(name string) *app                      { return &app{name: name, notWeb: true} }

// imageApps maps the last part of a Docker image name ("grafana" for
// "grafana/grafana-oss" is matched as "grafana-oss") to the app.
var imageApps = map[string]*app{
	"n8n":                 web(map[int]endpoint{5678: httpEP}, "n8n"),
	"grafana":             web(map[int]endpoint{3000: httpEP}, "Grafana"),
	"grafana-oss":         web(map[int]endpoint{3000: httpEP}, "Grafana"),
	"grafana-enterprise":  web(map[int]endpoint{3000: httpEP}, "Grafana"),
	"portainer-ce":        web(map[int]endpoint{9443: httpsEP, 9000: httpEP}, "Portainer"),
	"portainer-ee":        web(map[int]endpoint{9443: httpsEP, 9000: httpEP}, "Portainer"),
	"home-assistant":      web(map[int]endpoint{8123: httpEP}, "Home Assistant"),
	"homeassistant":       web(map[int]endpoint{8123: httpEP}, "Home Assistant"),
	"uptime-kuma":         web(map[int]endpoint{3001: httpEP}, "Uptime Kuma"),
	"pihole":              web(map[int]endpoint{80: {"http", "/admin"}, 443: {"https", "/admin"}}, "Pi-hole"),
	"adguardhome":         web(map[int]endpoint{3000: httpEP, 80: httpEP, 443: httpsEP}, "AdGuard Home"),
	"nextcloud":           web(map[int]endpoint{80: httpEP, 443: httpsEP}, "Nextcloud"),
	"jellyfin":            web(map[int]endpoint{8096: httpEP, 8920: httpsEP}, "Jellyfin"),
	"pms-docker":          web(map[int]endpoint{32400: {"http", "/web"}}, "Plex"),
	"plex":                web(map[int]endpoint{32400: {"http", "/web"}}, "Plex"),
	"prometheus":          web(map[int]endpoint{9090: httpEP}, "Prometheus"),
	"alertmanager":        web(map[int]endpoint{9093: httpEP}, "Alertmanager"),
	"netdata":             web(map[int]endpoint{19999: httpEP}, "Netdata"),
	"vaultwarden":         web(map[int]endpoint{80: httpEP}, "Vaultwarden"),
	"gitea":               web(map[int]endpoint{3000: httpEP}, "Gitea"),
	"forgejo":             web(map[int]endpoint{3000: httpEP}, "Forgejo"),
	"code-server":         web(map[int]endpoint{8080: httpEP, 8443: httpsEP}, "code-server"),
	"syncthing":           web(map[int]endpoint{8384: httpEP}, "Syncthing"),
	"qbittorrent":         web(map[int]endpoint{8080: httpEP}, "qBittorrent"),
	"transmission":        web(map[int]endpoint{9091: httpEP}, "Transmission"),
	"sonarr":              web(map[int]endpoint{8989: httpEP}, "Sonarr"),
	"radarr":              web(map[int]endpoint{7878: httpEP}, "Radarr"),
	"prowlarr":            web(map[int]endpoint{9696: httpEP}, "Prowlarr"),
	"heimdall":            web(map[int]endpoint{80: httpEP, 443: httpsEP}, "Heimdall"),
	"homepage":            web(map[int]endpoint{3000: httpEP}, "Homepage"),
	"dozzle":              web(map[int]endpoint{8080: httpEP}, "Dozzle"),
	"nginx-proxy-manager": web(map[int]endpoint{81: httpEP}, "Nginx Proxy Manager"),
	"traefik":             web(map[int]endpoint{8080: {"http", "/dashboard/"}}, "Traefik"),
	"minio":               web(map[int]endpoint{9001: httpEP}, "MinIO Console"),
	"pgadmin4":            web(map[int]endpoint{80: httpEP, 443: httpsEP}, "pgAdmin"),
	"adminer":             web(map[int]endpoint{8080: httpEP}, "Adminer"),
	"phpmyadmin":          web(map[int]endpoint{80: httpEP}, "phpMyAdmin"),
	"open-webui":          web(map[int]endpoint{8080: httpEP}, "Open WebUI"),
	"immich-server":       web(map[int]endpoint{2283: httpEP}, "Immich"),
	"paperless-ngx":       web(map[int]endpoint{8000: httpEP}, "Paperless-ngx"),
	"mealie":              web(map[int]endpoint{9000: httpEP}, "Mealie"),
	"wordpress":           web(map[int]endpoint{80: httpEP}, "WordPress"),
	"rabbitmq":            web(map[int]endpoint{15672: httpEP}, "RabbitMQ"),
	"kibana":              web(map[int]endpoint{5601: httpEP}, "Kibana"),
	"nginx":               web(map[int]endpoint{80: httpEP, 443: httpsEP}, "nginx"),
	"httpd":               web(map[int]endpoint{80: httpEP, 443: httpsEP}, "Apache"),
	"caddy":               web(map[int]endpoint{80: httpEP, 443: httpsEP}, "Caddy"),
	"postgres":            notWeb("PostgreSQL"),
	"postgis":             notWeb("PostgreSQL"),
	"mysql":               notWeb("MySQL"),
	"mariadb":             notWeb("MariaDB"),
	"redis":               notWeb("Redis"),
	"valkey":              notWeb("Valkey"),
	"mongo":               notWeb("MongoDB"),
	"memcached":           notWeb("Memcached"),
	"eclipse-mosquitto":   notWeb("Mosquitto (MQTT)"),
	"mosquitto":           notWeb("Mosquitto (MQTT)"),
	"elasticsearch":       notWeb("Elasticsearch"),
}

// appForImage finds the app for a short image name ("grafana/grafana-oss").
// Images named ".../server" are matched on their owner ("vaultwarden").
func appForImage(image string) *app {
	if image == "" {
		return nil
	}
	parts := strings.Split(strings.ToLower(image), "/")
	last := parts[len(parts)-1]
	if a := imageApps[last]; a != nil {
		return a
	}
	if last == "server" && len(parts) > 1 {
		return imageApps[parts[len(parts)-2]]
	}
	return nil
}

// portApps recognises programs by the port they listen on, when no Docker
// image says otherwise. Only ports that almost always mean that program.
var portApps = map[int]*app{
	5678:  web(map[int]endpoint{5678: httpEP}, "n8n"),
	8123:  web(map[int]endpoint{8123: httpEP}, "Home Assistant"),
	8006:  web(map[int]endpoint{8006: httpsEP}, "Proxmox"),
	9443:  web(map[int]endpoint{9443: httpsEP}, "Portainer"),
	3001:  web(map[int]endpoint{3001: httpEP}, "Uptime Kuma"),
	8096:  web(map[int]endpoint{8096: httpEP}, "Jellyfin"),
	32400: web(map[int]endpoint{32400: {"http", "/web"}}, "Plex"),
	19999: web(map[int]endpoint{19999: httpEP}, "Netdata"),
	8384:  web(map[int]endpoint{8384: httpEP}, "Syncthing"),
	631:   web(map[int]endpoint{631: httpEP}, "Printers (CUPS)"),
	22:    notWeb("SSH"),
	25:    notWeb("Mail (SMTP)"),
	53:    notWeb("DNS"),
	110:   notWeb("Mail (POP3)"),
	111:   notWeb("RPC"),
	139:   notWeb("Windows file sharing"),
	143:   notWeb("Mail (IMAP)"),
	445:   notWeb("Windows file sharing"),
	465:   notWeb("Mail (SMTP)"),
	587:   notWeb("Mail (SMTP)"),
	993:   notWeb("Mail (IMAP)"),
	995:   notWeb("Mail (POP3)"),
	1883:  notWeb("MQTT"),
	2049:  notWeb("NFS"),
	3306:  notWeb("MySQL"),
	3389:  notWeb("Remote Desktop"),
	5432:  notWeb("PostgreSQL"),
	5900:  notWeb("VNC"),
	6379:  notWeb("Redis"),
	11211: notWeb("Memcached"),
	27017: notWeb("MongoDB"),
}

// webPorts are the usual web ports, for programs nothing else recognises.
var webPorts = map[int]string{80: "http", 443: "https", 8080: "http", 8443: "https", 8000: "http", 3000: "http"}
