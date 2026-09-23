//go:build gomock || generate

package quic

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_send_conn_test.go github.com/aegispanel/nodeagent/internal/realityquic SendConn"
type SendConn = sendConn

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_raw_conn_test.go github.com/aegispanel/nodeagent/internal/realityquic RawConn"
type RawConn = rawConn

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_sender_test.go github.com/aegispanel/nodeagent/internal/realityquic Sender"
type Sender = sender

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_stream_sender_test.go github.com/aegispanel/nodeagent/internal/realityquic StreamSender"
type StreamSender = streamSender

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_stream_control_frame_getter_test.go github.com/aegispanel/nodeagent/internal/realityquic StreamControlFrameGetter"
type StreamControlFrameGetter = streamControlFrameGetter

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_stream_frame_getter_test.go github.com/aegispanel/nodeagent/internal/realityquic StreamFrameGetter"
type StreamFrameGetter = streamFrameGetter

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_frame_source_test.go github.com/aegispanel/nodeagent/internal/realityquic FrameSource"
type FrameSource = frameSource

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_ack_frame_source_test.go github.com/aegispanel/nodeagent/internal/realityquic AckFrameSource"
type AckFrameSource = ackFrameSource

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_sealing_manager_test.go github.com/aegispanel/nodeagent/internal/realityquic SealingManager"
type SealingManager = sealingManager

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_unpacker_test.go github.com/aegispanel/nodeagent/internal/realityquic Unpacker"
type Unpacker = unpacker

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_packer_test.go github.com/aegispanel/nodeagent/internal/realityquic Packer"
type Packer = packer

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_mtu_discoverer_test.go github.com/aegispanel/nodeagent/internal/realityquic MTUDiscoverer"
type MTUDiscoverer = mtuDiscoverer

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_conn_runner_test.go github.com/aegispanel/nodeagent/internal/realityquic ConnRunner"
type ConnRunner = connRunner

//go:generate sh -c "go tool mockgen -typed -build_flags=\"-tags=gomock\" -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_packet_handler_test.go github.com/aegispanel/nodeagent/internal/realityquic PacketHandler"
type PacketHandler = packetHandler

//go:generate sh -c "go tool mockgen -typed -package quic -self_package github.com/aegispanel/nodeagent/internal/realityquic -self_package github.com/aegispanel/nodeagent/internal/realityquic -destination mock_packetconn_test.go net PacketConn"
