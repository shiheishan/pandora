import { record, type Row } from "./data";
export const protocolLabels:Record<string,string>={
 "network_settings.mode":"XHTTP工作模式",ws_path:"WebSocket路径",grpc_path:"gRPC路径",headers:"额外HTTP请求头",sc_max_each_post_bytes:"单次POST大小（字节）",sc_min_posts_interval_ms:"POST间隔（毫秒）",sc_stream_up_server_secs:"上行连接持续时间（秒）",uplink_chunk_size:"上行分块大小（字节）",sc_max_buffered_posts:"缓冲POST数量",server_max_header_bytes:"请求头最大字节数",session_placement:"会话标识位置",session_key:"会话标识名称",seq_placement:"序号位置",seq_key:"序号名称",uplink_http_method:"上行HTTP方法",uplink_data_placement:"上行数据位置",uplink_data_key:"上行数据名称",mtu:"MTU（字节）",tti:"发送间隔（毫秒）",uplink_capacity:"上行容量",downlink_capacity:"下行容量",congestion:"拥塞控制",read_buffer_size:"读取缓冲",write_buffer_size:"写入缓冲",mask:"流量掩码",mask_password:"掩码密码",padding_scheme:"AnyTLS填充规则",udp_timeout:"UDP超时",congestion_control:"拥塞控制算法",transport:"传输方式",version:"协议版本",server:"握手服务器",handshake_server:"握手域名",server_port:"握手端口",method:"加密方式",strict:"严格模式",wildcard_sni:"动态SNI模式",sni:"TLS服务器名称",alpn:"ALPN",flow:"流控模式",certificate:"证书",private_key:"私钥"};
export const rangeKeys=["sc_max_each_post_bytes","sc_min_posts_interval_ms","sc_stream_up_server_secs","uplink_chunk_size"];
export function protocolInputConfig(value:unknown):Row {
 const config={...record(value)};
 if(typeof config.headers==="string"){
  const headers:Record<string,string>={};
  for(const line of config.headers.split(/\r?\n/).filter(v=>v.trim())){
   const at=line.indexOf(":");if(at<=0)throw new Error("请求头请按每行 名称: 值 填写");
   const key=line.slice(0,at).trim(),val=line.slice(at+1).trim();
   if(!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(key))throw new Error("请求头名称不正确");
   if(Object.keys(headers).some(k=>k.toLowerCase()===key.toLowerCase()))throw new Error("请求头名称不能重复");headers[key]=val;
  }
  if(Object.keys(headers).length)config.headers=headers;else delete config.headers;
 }
 for(const key of rangeKeys){if(config[key]&&typeof config[key]==="object"){
  const r=record(config[key]);if(r.from==null&&r.to==null){delete config[key];continue;}
  if(!Number.isSafeInteger(r.from)||!Number.isSafeInteger(r.to)||Number(r.from)>Number(r.to))throw new Error(`${protocolLabels[key]}：请填写完整且有序的最小值、最大值`);
 }}
 if(typeof config.padding_scheme==="string"){
  const lines=config.padding_scheme.split(/\r?\n/).map(v=>v.trim()).filter(Boolean);
  if(lines.length)config.padding_scheme=lines;else delete config.padding_scheme;
 }
 return config;
}
