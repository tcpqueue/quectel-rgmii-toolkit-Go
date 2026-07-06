//go:build linux
// +build linux

package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	nativeConsoleUsername         = "root"
	nativeConsolePassword         = "admin321"
	nativeConsoleMaxLoginAttempts = 3
)

const nativeConsoleHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Terminal</title>
<style>
html,body{height:100%;margin:0;overflow:hidden;background:#050505;color:#f2f2f2;font-family:Consolas,Menlo,monospace}
body{display:flex;flex-direction:column;min-height:0}
#bar{flex:0 0 auto;min-height:40px;box-sizing:border-box;padding:9px 14px;background:#171717;border-bottom:1px solid #333;display:flex;gap:12px;align-items:center}
#state{font-size:14px;color:#9ad}
#hint{font-size:12px;color:#aaa}
#term{flex:1 1 auto;min-height:0;height:auto;box-sizing:border-box;overflow:auto;scrollbar-width:none;-ms-overflow-style:none;padding:12px;white-space:pre-wrap;word-break:break-word;outline:none;font-size:15px;line-height:1.35;background:#050505}
#term::-webkit-scrollbar{width:0;height:0}
.cursor::after{content:"_";animation:blink 1s steps(1) infinite}@keyframes blink{50%{opacity:0}}
</style>
</head>
<body>
<div id="bar"><strong>Terminal</strong><span id="state">connecting...</span><span id="hint">Click here and type. Ctrl+C / Ctrl+D / arrows are supported.</span></div>
<pre id="term" class="cursor" tabindex="0"></pre>
<script>
(function(){
  var term=document.getElementById('term');
  var state=document.getElementById('state');
  var decoder=new TextDecoder('utf-8');
  var encoder=new TextEncoder();
  var scheme=location.protocol==='https:'?'wss':'ws';
  var ws=new WebSocket(scheme+'://'+location.host+'/api/console/ws');
  ws.binaryType='arraybuffer';

  function setState(text){ state.textContent=text; }
  function scrollBottom(){ term.scrollTop=term.scrollHeight; }
  var ansiState={fg:'',bg:'',bold:false,dim:false,underline:false};
  var ansiCarry='';
  var ansiColors={
    30:'#000000',31:'#cc5555',32:'#55cc55',33:'#cccc55',34:'#5555cc',35:'#cc55cc',36:'#55cccc',37:'#dddddd',
    90:'#777777',91:'#ff7777',92:'#77ff77',93:'#ffff77',94:'#7777ff',95:'#ff77ff',96:'#77ffff',97:'#ffffff',
    40:'#000000',41:'#5f1f1f',42:'#1f5f1f',43:'#5f5f1f',44:'#1f1f5f',45:'#5f1f5f',46:'#1f5f5f',47:'#dddddd',
    100:'#555555',101:'#7f3333',102:'#337f33',103:'#7f7f33',104:'#33337f',105:'#7f337f',106:'#337f7f',107:'#ffffff'
  };
  var ansi256=[
    '#000000','#800000','#008000','#808000','#000080','#800080','#008080','#c0c0c0',
    '#808080','#ff0000','#00ff00','#ffff00','#0000ff','#ff00ff','#00ffff','#ffffff'
  ];
  function xterm256Color(n){
    n=Number(n);
    if(n>=0 && n<16) return ansi256[n];
    if(n>=16 && n<=231){
      var c=n-16;
      var r=Math.floor(c/36), g=Math.floor((c%36)/6), b=c%6;
      var conv=function(v){ return v===0?0:55+v*40; };
      return 'rgb('+conv(r)+','+conv(g)+','+conv(b)+')';
    }
    if(n>=232 && n<=255){
      var level=8+(n-232)*10;
      return 'rgb('+level+','+level+','+level+')';
    }
    return '';
  }
  function resetAnsi(){ ansiState={fg:'',bg:'',bold:false,dim:false,underline:false}; }
  function applySgr(params){
    if(!params.length) params=['0'];
    for(var i=0;i<params.length;i++){
      var raw=params[i];
      var code=raw===''?0:Number(raw);
      if(!isFinite(code)) continue;
      if(code===0){ resetAnsi(); }
      else if(code===1){ ansiState.bold=true; ansiState.dim=false; }
      else if(code===2){ ansiState.dim=true; }
      else if(code===4){ ansiState.underline=true; }
      else if(code===22){ ansiState.bold=false; ansiState.dim=false; }
      else if(code===24){ ansiState.underline=false; }
      else if(code===39){ ansiState.fg=''; }
      else if(code===49){ ansiState.bg=''; }
      else if((code>=30 && code<=37) || (code>=90 && code<=97)){ ansiState.fg=ansiColors[code] || ''; }
      else if((code>=40 && code<=47) || (code>=100 && code<=107)){ ansiState.bg=ansiColors[code] || ''; }
      else if((code===38 || code===48) && params[i+1]==='5' && i+2<params.length){
        var color=xterm256Color(params[i+2]);
        if(code===38) ansiState.fg=color; else ansiState.bg=color;
        i+=2;
      }else if((code===38 || code===48) && params[i+1]==='2' && i+4<params.length){
        var r=Number(params[i+2]), g=Number(params[i+3]), b=Number(params[i+4]);
        if(isFinite(r) && isFinite(g) && isFinite(b)){
          var rgb='rgb('+Math.max(0,Math.min(255,r))+','+Math.max(0,Math.min(255,g))+','+Math.max(0,Math.min(255,b))+')';
          if(code===38) ansiState.fg=rgb; else ansiState.bg=rgb;
        }
        i+=4;
      }
    }
  }
  function spanHasStyle(){ return ansiState.fg || ansiState.bg || ansiState.bold || ansiState.dim || ansiState.underline; }
  function appendStyledText(text){
    if(!text) return;
    var node;
    if(spanHasStyle()){
      node=document.createElement('span');
      if(ansiState.fg) node.style.color=ansiState.fg;
      if(ansiState.bg) node.style.backgroundColor=ansiState.bg;
      if(ansiState.bold) node.style.fontWeight='700';
      if(ansiState.dim) node.style.opacity='0.65';
      if(ansiState.underline) node.style.textDecoration='underline';
      node.textContent=text;
    }else{
      node=document.createTextNode(text);
    }
    term.appendChild(node);
  }
  function removeLastCharacter(){
    var node=term.lastChild;
    while(node){
      var text=node.textContent || '';
      if(text.length>0){
        node.textContent=text.slice(0,-1);
        if(!node.textContent && node.parentNode) node.parentNode.removeChild(node);
        return;
      }
      var prev=node.previousSibling;
      if(node.parentNode) node.parentNode.removeChild(node);
      node=prev;
    }
  }
  function clearTerm(){
    term.textContent='';
    resetAnsi();
  }
  function pruneTerm(){
    if(term.textContent.length<=200000) return;
    term.textContent=term.textContent.slice(-120000);
  }
  function appendPlain(s){
    var start=0;
    for(var i=0;i<s.length;i++){
      var ch=s[i];
      if(ch==='\b' || ch==='\x7f' || ch==='\f'){
        if(i>start) appendStyledText(s.slice(start,i));
        if(ch==='\f') clearTerm(); else removeLastCharacter();
        start=i+1;
      }
    }
    if(start<s.length) appendStyledText(s.slice(start));
  }
  function appendText(s){
    s=(ansiCarry+s).replace(/\r/g,'');
    ansiCarry='';
    var i=0;
    while(i<s.length){
      var esc=s.indexOf('\x1b',i);
      if(esc<0){ appendPlain(s.slice(i)); break; }
      if(esc>i) appendPlain(s.slice(i,esc));
      if(esc+1>=s.length){ ansiCarry=s.slice(esc); break; }
      var next=s[esc+1];
      if(next==='['){
        var end=esc+2;
        while(end<s.length && (s.charCodeAt(end)<0x40 || s.charCodeAt(end)>0x7e)) end++;
        if(end>=s.length){ ansiCarry=s.slice(esc); break; }
        var final=s[end];
        if(final==='m') applySgr(s.slice(esc+2,end).split(';'));
        i=end+1;
      }else if(next===']'){
        var bel=s.indexOf('\x07',esc+2);
        var st=s.indexOf('\x1b\\',esc+2);
        var oscEnd=-1;
        if(bel>=0 && st>=0) oscEnd=Math.min(bel,st+1);
        else if(bel>=0) oscEnd=bel;
        else if(st>=0) oscEnd=st+1;
        if(oscEnd<0){ ansiCarry=s.slice(esc); break; }
        i=oscEnd+1;
      }else if(next==='(' || next===')'){
        if(esc+2>=s.length){ ansiCarry=s.slice(esc); break; }
        i=esc+3;
      }else{
        i=esc+2;
      }
    }
    pruneTerm();
    scrollBottom();
  }
  function sendBytes(text){
    if(ws.readyState===WebSocket.OPEN) ws.send(encoder.encode(text));
  }
  ws.onopen=function(){ setState('connected'); term.focus(); };
  ws.onclose=function(){ setState('closed'); appendText('\n[console closed]\n'); };
  ws.onerror=function(){ setState('error'); };
  ws.onmessage=function(ev){
    if(ev.data instanceof ArrayBuffer){ appendText(decoder.decode(ev.data,{stream:true})); }
    else { appendText(String(ev.data)); }
  };

  document.addEventListener('keydown',function(e){
    if(document.activeElement!==term) term.focus();
    var v=null;
    if(e.ctrlKey){
      var k=e.key.toLowerCase();
      if(k==='c') v='\x03';
      else if(k==='d') v='\x04';
      else if(k==='l') v='\x0c';
      else if(k==='z') v='\x1a';
    }else if(e.key==='Enter') v='\r';
    else if(e.key==='Backspace') v='\x7f';
    else if(e.key==='Tab') v='\t';
    else if(e.key==='ArrowUp') v='\x1b[A';
    else if(e.key==='ArrowDown') v='\x1b[B';
    else if(e.key==='ArrowRight') v='\x1b[C';
    else if(e.key==='ArrowLeft') v='\x1b[D';
    else if(e.key==='Home') v='\x1b[H';
    else if(e.key==='End') v='\x1b[F';
    else if(e.key==='Delete') v='\x1b[3~';
    else if(e.key==='PageUp') v='\x1b[5~';
    else if(e.key==='PageDown') v='\x1b[6~';
    else if(e.key.length===1) v=e.key;
    if(v!==null){ e.preventDefault(); sendBytes(v); }
  });
  term.addEventListener('paste',function(e){
    e.preventDefault();
    var text=(e.clipboardData||window.clipboardData).getData('text') || '';
    sendBytes(text.replace(/\r?\n/g,'\r'));
  });
  term.addEventListener('click',function(){ term.focus(); });
})();
</script>
</body>
</html>
`

type nativeWSConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func (s *simpleAdminServer) handleNativeConsole(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/console" && r.URL.Path != "/console/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(nativeConsoleHTML))
}

func (s *simpleAdminServer) handleNativeConsoleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !consoleWebSocketOriginAllowed(r) {
		http.Error(w, "websocket origin forbidden", http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") == "" {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack unavailable", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	ws := &nativeWSConn{conn: conn, br: rw.Reader}
	accept := websocketAcceptKey(r.Header.Get("Sec-WebSocket-Key"))
	_, _ = rw.Writer.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.Writer.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.Writer.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.Writer.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err := rw.Writer.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	defer conn.Close()

	if !authenticateNativeConsole(ws) {
		return
	}

	shell, err := startNativeConsoleShell()
	if err != nil {
		_ = ws.writeBinary([]byte("failed to start shell: " + err.Error() + "\n"))
		_ = ws.writeClose()
		return
	}
	defer shell.close()

	consoleModel, _ := s.fetchStandaloneModel(false)
	if consoleModel == "-" || strings.TrimSpace(consoleModel) == "" {
		consoleModel = "SimpleAdmin"
	}
	_ = ws.writeBinary([]byte("==============================================================\r\n"))
	_ = ws.writeBinary([]byte(consoleModel + " native Go console\r\n"))
	_ = ws.writeBinary([]byte("AT channel: /dev/smd11 is used by " + consoleModel + "\r\n"))
	_ = ws.writeBinary([]byte("==============================================================\r\n"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := shell.master.Read(buf)
			if n > 0 {
				if writeErr := ws.writeBinary(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		opcode, payload, err := ws.readClientFrame()
		if err != nil {
			return
		}
		switch opcode {
		case 0x1, 0x2, 0x0:
			if len(payload) > 0 {
				if _, err := shell.master.Write(payload); err != nil {
					return
				}
			}
		case 0x8:
			_ = ws.writeClose()
			return
		case 0x9:
			_ = ws.writeFrame(0xA, payload)
		case 0xA:
		default:
		}

		select {
		case <-done:
			return
		default:
		}
	}
}

func consoleWebSocketOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	if u.Scheme != expectedScheme {
		return false
	}
	return normalizeOriginHost(u.Host) == normalizeOriginHost(r.Host)
}

func normalizeOriginHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if strings.HasSuffix(host, ":80") {
		return strings.TrimSuffix(host, ":80")
	}
	if strings.HasSuffix(host, ":443") {
		return strings.TrimSuffix(host, ":443")
	}
	return host
}

func authenticateNativeConsole(ws *nativeWSConn) bool {
	_ = ws.writeBinary([]byte("Terminal login required\r\n"))
	for attempt := 0; attempt < nativeConsoleMaxLoginAttempts; attempt++ {
		username, ok := readNativeConsoleLine(ws, "login: ", false)
		if !ok {
			return false
		}
		password, ok := readNativeConsoleLine(ws, "Password: ", true)
		if !ok {
			return false
		}
		if constantTimeEqual(strings.TrimSpace(username), nativeConsoleUsername) && constantTimeEqual(password, nativeConsolePassword) {
			_ = ws.writeBinary([]byte("\r\n"))
			return true
		}
		_ = ws.writeBinary([]byte("\r\nLogin incorrect\r\n"))
	}
	_ = ws.writeBinary([]byte("Too many failed login attempts\r\n"))
	_ = ws.writeClose()
	return false
}

func readNativeConsoleLine(ws *nativeWSConn, prompt string, hidden bool) (string, bool) {
	var builder strings.Builder
	if err := ws.writeBinary([]byte(prompt)); err != nil {
		return "", false
	}
	for {
		opcode, payload, err := ws.readClientFrame()
		if err != nil {
			return "", false
		}
		switch opcode {
		case 0x1, 0x2, 0x0:
			done, err := appendNativeConsoleLineInput(&builder, payload, hidden, ws.writeBinary)
			if err != nil {
				return "", false
			}
			if done {
				return builder.String(), true
			}
		case 0x8:
			_ = ws.writeClose()
			return "", false
		case 0x9:
			_ = ws.writeFrame(0xA, payload)
		case 0xA:
		default:
		}
	}
}

func appendNativeConsoleLineInput(builder *strings.Builder, payload []byte, hidden bool, echo func([]byte) error) (bool, error) {
	for _, b := range payload {
		switch b {
		case '\r', '\n':
			if err := echo([]byte("\r\n")); err != nil {
				return false, err
			}
			return true, nil
		case '\b', 0x7f:
			current := builder.String()
			if current == "" {
				continue
			}
			runes := []rune(current)
			builder.Reset()
			builder.WriteString(string(runes[:len(runes)-1]))
			if !hidden {
				if err := echo([]byte("\b \b")); err != nil {
					return false, err
				}
			}
		case 0x03:
			builder.Reset()
			if err := echo([]byte("^C\r\n")); err != nil {
				return false, err
			}
			return true, nil
		default:
			if b < 0x20 {
				continue
			}
			builder.WriteByte(b)
			if !hidden {
				if err := echo([]byte{b}); err != nil {
					return false, err
				}
			}
		}
	}
	return false, nil
}

func websocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(key) + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (ws *nativeWSConn) readClientFrame() (byte, []byte, error) {
	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(ws.br, header); err != nil {
			return 0, nil, err
		}
		opcode := header[0] & 0x0f
		masked := header[1]&0x80 != 0
		length := uint64(header[1] & 0x7f)
		switch length {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(ws.br, ext); err != nil {
				return 0, nil, err
			}
			length = uint64(binary.BigEndian.Uint16(ext))
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(ws.br, ext); err != nil {
				return 0, nil, err
			}
			length = binary.BigEndian.Uint64(ext)
		}
		if length > 1<<20 {
			return 0, nil, fmt.Errorf("websocket frame too large: %d", length)
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(ws.br, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(ws.br, payload); err != nil {
				return 0, nil, err
			}
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		return opcode, payload, nil
	}
}

func (ws *nativeWSConn) writeBinary(payload []byte) error {
	return ws.writeFrame(0x2, payload)
}

func (ws *nativeWSConn) writeClose() error {
	return ws.writeFrame(0x8, nil)
}

func (ws *nativeWSConn) writeFrame(opcode byte, payload []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xffff:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}
	if _, err := ws.conn.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := ws.conn.Write(payload)
	return err
}

type nativeConsoleShell struct {
	master *os.File
	cmd    *exec.Cmd
}

func startNativeConsoleShell() (*nativeConsoleShell, error) {
	master, slave, err := openConsolePTY()
	if err != nil {
		return nil, err
	}
	setPTYWindowSize(master.Fd(), 32, 120)

	shellPath := nativeConsoleShellPath()
	cmd := exec.Command(shellPath)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "SHELL="+shellPath)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, err
	}
	_ = slave.Close()

	shell := &nativeConsoleShell{master: master, cmd: cmd}
	go func() {
		_ = cmd.Wait()
		_ = master.Close()
	}()
	return shell, nil
}

func (s *nativeConsoleShell) close() {
	if s == nil {
		return
	}
	if s.master != nil {
		_ = s.master.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGHUP)
		time.Sleep(100 * time.Millisecond)
		_ = s.cmd.Process.Kill()
	}
}

func nativeConsoleShellPath() string {
	for _, shell := range []string{"/bin/sh", "/usr/bin/sh", "/bin/ash"} {
		if st, err := os.Stat(shell); err == nil && !st.IsDir() {
			return shell
		}
	}
	return "/bin/sh"
}

func openConsolePTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, nil, err
	}

	unlock := int32(0)
	if err := ioctl(master.Fd(), tioCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		_ = master.Close()
		return nil, nil, err
	}

	var ptyNumber uint32
	if err := ioctl(master.Fd(), tioCGPTN, uintptr(unsafe.Pointer(&ptyNumber))); err != nil {
		_ = master.Close()
		return nil, nil, err
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", ptyNumber)
	_ = os.Chown(slavePath, 0, 20)
	_ = os.Chmod(slavePath, 0620)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0600)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func setPTYWindowSize(fd uintptr, rows, cols uint16) {
	ws := winsize{Row: rows, Col: cols}
	if err := ioctl(fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); err != nil && !errors.Is(err, syscall.ENOTTY) {
		log.Printf("设置控制台窗口大小失败: %v", err)
	}
}
