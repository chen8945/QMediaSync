package database

// Config 保存 PostgreSQL 的连接和连接池参数。
type Config struct {
	Host         string
	Port         int
	User         string
	Password     string
	DBName       string
	SSLMode      string
	MaxOpenConns int
	MaxIdleConns int
}
