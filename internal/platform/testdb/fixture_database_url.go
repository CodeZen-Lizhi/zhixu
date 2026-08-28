package testdb

// DatabaseURL 返回该 fixture 独立数据库的完整连接串。仅用于需要把 DSN 传给
// 子进程或外部工具的测试（例如 River SIGKILL rescue smoke）；输出含凭据，
// 不得写入日志或对外暴露。
func (f *Fixture) DatabaseURL() string {
	if f == nil {
		return ""
	}
	return f.databaseURL
}
