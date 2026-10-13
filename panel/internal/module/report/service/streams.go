package service

// UseStreams 装配 SSE 连接计数（serve 启动 Hub 后调用）。
func (s *Service) UseStreams(c StreamCounter) {
	s.streams = c
}
