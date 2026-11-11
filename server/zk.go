package server

// بروتوكول أجهزة ZKTeco عبر TCP
// منقول من مكتبة pyzk (https://github.com/fananimi/pyzk) وهي المرجع الأوسع استخداماً.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	cmdUserWrq       = 8
	cmdUserTempRrq   = 9
	cmdOptionsRrq    = 11
	cmdAttLogRrq     = 13
	cmdClearAttLog   = 15
	cmdUnlock        = 31
	cmdGetFreeSizes  = 50
	cmdWriteLCD      = 66
	cmdClearLCD      = 67
	cmdGetPinWidth   = 69
	cmdGetTime       = 201
	cmdSetTime       = 202
	cmdRegEvent      = 500
	cmdConnect       = 1000
	cmdExit          = 1001
	cmdEnableDevice  = 1002
	cmdDisableDevice = 1003
	cmdRestart       = 1004
	cmdPowerOff      = 1005
	cmdRefreshData   = 1013
	cmdTestVoice     = 1017
	cmdGetVersion    = 1100
	cmdAuth          = 1102
	cmdPrepareData   = 1500
	cmdData          = 1501
	cmdFreeData      = 1502
	cmdPrepareBuffer = 1503
	cmdReadBuffer    = 1504
	ackOK            = 2000
	ackUnauth        = 2005
	fctUser          = 5
	ushrtMax         = 65535
)

var le = binary.LittleEndian

type zkClient struct {
	conn    net.Conn
	r       *bufio.Reader
	session uint16
	replyID uint16
	timeout time.Duration
}

var errCommKey = errors.New("الجهاز رفض الاتصال: عليه كلمة سر اتصال (Comm Key). اكتبها بخانة \"كلمة سر الاتصال\" أو خليها 0 من قائمة الجهاز")

func dialZK(ip string, port int, commKey uint32, timeout time.Duration) (*zkClient, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return nil, fmt.Errorf("ما گدرت أتصل بالجهاز %s:%d (%v)", ip, port, err)
	}
	c := &zkClient{conn: conn, r: bufio.NewReaderSize(conn, 1<<16), replyID: ushrtMax - 1, timeout: timeout}
	code, _, err := c.send(cmdConnect, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if code == ackUnauth {
		code, _, err = c.send(cmdAuth, makeCommKey(commKey, c.session, 50))
		if err != nil {
			conn.Close()
			return nil, err
		}
	}
	if code != ackOK {
		conn.Close()
		if code == ackUnauth {
			return nil, errCommKey
		}
		return nil, fmt.Errorf("الجهاز رفض الاتصال (رمز الرد %d)", code)
	}
	return c, nil
}

func (c *zkClient) close() {
	c.send(cmdExit, nil)
	c.conn.Close()
}

// مجموع التحقق، نفس خوارزمية zkemsdk.c كما في pyzk
func zkChecksum(p []byte) uint16 {
	chk := 0
	i, l := 0, len(p)
	for l > 1 {
		chk += int(p[i]) | int(p[i+1])<<8
		i += 2
		if chk > ushrtMax {
			chk -= ushrtMax
		}
		l -= 2
	}
	if l > 0 {
		chk += int(p[len(p)-1])
	}
	for chk > ushrtMax {
		chk -= ushrtMax
	}
	chk = ^chk
	for chk < 0 {
		chk += ushrtMax
	}
	return uint16(chk)
}

func (c *zkClient) header(cmd uint16, data []byte) []byte {
	buf := make([]byte, 8+len(data))
	le.PutUint16(buf[0:], cmd)
	le.PutUint16(buf[4:], c.session)
	le.PutUint16(buf[6:], c.replyID)
	copy(buf[8:], data)
	chk := zkChecksum(buf)
	rid := int(c.replyID) + 1
	if rid >= ushrtMax {
		rid -= ushrtMax
	}
	c.replyID = uint16(rid)
	le.PutUint16(buf[2:], chk)
	le.PutUint16(buf[6:], c.replyID)
	return buf
}

func (c *zkClient) readFrame() (code, session, replyID uint16, data []byte, err error) {
	c.conn.SetReadDeadline(time.Now().Add(c.timeout))
	var top [8]byte
	if _, err = io.ReadFull(c.r, top[:]); err != nil {
		return 0, 0, 0, nil, fmt.Errorf("انقطع الاتصال بالجهاز أو ما رد بالوقت (%v)", err)
	}
	if le.Uint16(top[0:]) != 0x5050 || le.Uint16(top[2:]) != 0x7D82 {
		return 0, 0, 0, nil, errors.New("وصلت بيانات غير صالحة من الجهاز")
	}
	n := le.Uint32(top[4:])
	if n < 8 || n > 32<<20 {
		return 0, 0, 0, nil, fmt.Errorf("طول حزمة غير صالح من الجهاز (%d)", n)
	}
	body := make([]byte, n)
	if _, err = io.ReadFull(c.r, body); err != nil {
		return 0, 0, 0, nil, fmt.Errorf("انقطع الاتصال أثناء استلام البيانات (%v)", err)
	}
	return le.Uint16(body[0:]), le.Uint16(body[4:]), le.Uint16(body[6:]), body[8:], nil
}

// يرسل أمر ويرجع رمز الرد والبيانات
func (c *zkClient) send(cmd uint16, data []byte) (uint16, []byte, error) {
	pkt := c.header(cmd, data)
	frame := make([]byte, 8+len(pkt))
	le.PutUint16(frame[0:], 0x5050)
	le.PutUint16(frame[2:], 0x7D82)
	le.PutUint32(frame[4:], uint32(len(pkt)))
	copy(frame[8:], pkt)
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	if _, err := c.conn.Write(frame); err != nil {
		return 0, nil, fmt.Errorf("ما گدرت أرسل للجهاز (%v)", err)
	}
	for {
		code, sess, rid, d, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		if code == cmdRegEvent { // حدث حي غير مطلوب
			continue
		}
		if cmd == cmdConnect {
			c.session = sess
		}
		c.replyID = rid
		return code, d, nil
	}
}

func isSuccess(code uint16) bool { return code == ackOK || code == cmdPrepareData || code == cmdData }

// أمر يجب أن ينجح
func (c *zkClient) cmd(cmd uint16, data []byte) ([]byte, error) {
	code, d, err := c.send(cmd, data)
	if err != nil {
		return nil, err
	}
	if !isSuccess(code) {
		if code == ackUnauth {
			return nil, errCommKey
		}
		return nil, fmt.Errorf("الجهاز رفض الأمر أو لا يدعمه (رمز الرد %d)", code)
	}
	return d, nil
}

// قراءة بيانات كبيرة على دفعات (مثل pyzk.read_with_buffer)
func (c *zkClient) readWithBuffer(command uint16, fct, ext uint32) ([]byte, error) {
	const maxChunk = 0xFFC0
	cs := make([]byte, 11)
	cs[0] = 1
	le.PutUint16(cs[1:], command)
	le.PutUint32(cs[3:], fct)
	le.PutUint32(cs[7:], ext)
	code, d, err := c.send(cmdPrepareBuffer, cs)
	if err != nil {
		return nil, err
	}
	if !isSuccess(code) {
		return nil, fmt.Errorf("الجهاز لا يدعم قراءة البيانات بهذه الطريقة (رمز الرد %d)", code)
	}
	if code == cmdData {
		return d, nil
	}
	if len(d) < 5 {
		return nil, errors.New("رد غير صالح لحجم البيانات")
	}
	size := int(le.Uint32(d[1:5]))
	out := make([]byte, 0, size)
	for start := 0; start < size; {
		n := min(maxChunk, size-start)
		chunk, err := c.readChunk(start, n)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		start += n
	}
	c.send(cmdFreeData, nil)
	return out, nil
}

func (c *zkClient) readChunk(start, size int) ([]byte, error) {
	var lastErr error
	for try := 0; try < 3; try++ {
		cs := make([]byte, 8)
		le.PutUint32(cs[0:], uint32(start))
		le.PutUint32(cs[4:], uint32(size))
		code, d, err := c.send(cmdReadBuffer, cs)
		if err != nil {
			return nil, err
		}
		switch code {
		case cmdData:
			return d, nil
		case cmdPrepareData:
			if len(d) < 4 {
				lastErr = errors.New("رد غير صالح من الجهاز")
				continue
			}
			want := int(le.Uint32(d[0:4]))
			buf := make([]byte, 0, want)
			broken := false
			for len(buf) < want {
				fc, _, _, fd, err := c.readFrame()
				if err != nil {
					return nil, err
				}
				if fc == cmdRegEvent {
					continue
				}
				if fc != cmdData {
					lastErr = fmt.Errorf("رد غير متوقع أثناء استلام البيانات (%d)", fc)
					broken = true
					break
				}
				buf = append(buf, fd...)
			}
			if broken {
				continue
			}
			fc, _, _, _, err := c.readFrame()
			if err != nil {
				return nil, err
			}
			if fc != ackOK {
				lastErr = fmt.Errorf("الجهاز ما أكد استلام الدفعة (%d)", fc)
				continue
			}
			return buf[:want], nil
		default:
			lastErr = fmt.Errorf("الجهاز رفض قراءة الدفعة (رمز الرد %d)", code)
		}
	}
	return nil, lastErr
}

// ---------- بيانات الجهاز ----------

type zkSizes struct {
	Users, Fingers, Records, Cards                 int
	FingersCap, UsersCap, RecordsCap               int
	FingersAv, UsersAv, RecordsAv, Faces, FacesCap int
	HasFaces                                       bool
}

func (c *zkClient) readSizes() (*zkSizes, error) {
	d, err := c.cmd(cmdGetFreeSizes, nil)
	if err != nil {
		return nil, err
	}
	if len(d) < 80 {
		return nil, errors.New("رد الذاكرة غير مكتمل")
	}
	f := func(i int) int { return int(int32(le.Uint32(d[i*4:]))) }
	s := &zkSizes{Users: f(4), Fingers: f(6), Records: f(8), Cards: f(12), FingersCap: f(14), UsersCap: f(15),
		RecordsCap: f(16), FingersAv: f(17), UsersAv: f(18), RecordsAv: f(19)}
	if len(d) >= 92 {
		s.HasFaces = true
		s.Faces = int(int32(le.Uint32(d[80:])))
		s.FacesCap = int(int32(le.Uint32(d[88:])))
	}
	return s, nil
}

type zkUser struct {
	UID      uint16
	Role     int
	Password []byte
	Name     string
	NameEnc  string // ascii | utf8 | cp1256
	Card     uint32
	UserID   string
	Raw      []byte // السجل الخام (72 بايت) حتى نعدل حقل واحد بدون ما نمس الباقي
}

func cstrBytes(b []byte) []byte {
	for i, x := range b {
		if x == 0 {
			return b[:i]
		}
	}
	return b
}

func (c *zkClient) getUsers() ([]zkUser, error) {
	sz, err := c.readSizes()
	if err != nil {
		return nil, err
	}
	if sz.Users == 0 {
		return []zkUser{}, nil
	}
	data, err := c.readWithBuffer(cmdUserTempRrq, fctUser, 0)
	if err != nil {
		return nil, err
	}
	if len(data) <= 4 {
		return []zkUser{}, nil
	}
	total := int(le.Uint32(data[0:4]))
	data = data[4:]
	pkt := 72
	if sz.Users > 0 && total%sz.Users == 0 {
		pkt = total / sz.Users
	}
	if pkt != 28 && pkt != 72 && pkt != 120 {
		switch {
		case total%72 == 0:
			pkt = 72
		case total%28 == 0:
			pkt = 28
		case total%120 == 0:
			pkt = 120
		default:
			pkt = 72
		}
	}
	users := []zkUser{}
	for len(data) >= pkt {
		r := data[:pkt]
		data = data[pkt:]
		u := zkUser{Raw: append([]byte(nil), r...)}
		switch pkt {
		case 28: // '<HB5s8sIxBhI'
			u.UID, u.Role = le.Uint16(r[0:]), int(r[2])
			u.Password = append([]byte(nil), cstrBytes(r[3:8])...)
			u.Name, u.NameEnc = decodeName(r[8:16])
			u.Card = le.Uint32(r[16:])
			u.UserID = strconv.FormatUint(uint64(le.Uint32(r[24:])), 10)
		case 120:
			u.UID, u.Role = le.Uint16(r[0:]), int(r[2])
			u.Password = append([]byte(nil), cstrBytes(r[3:11])...)
			first, e1 := decodeName(r[35:59])
			last, _ := decodeName(r[59:83])
			u.Name, u.NameEnc = trimSpace(first+" "+last), e1
			u.Card = le.Uint32(r[83:])
			u.UserID = trimSpace(string(cstrBytes(r[96:120])))
		default: // 72: '<HB8s24sIx7sx24s'
			u.UID, u.Role = le.Uint16(r[0:]), int(r[2])
			u.Password = append([]byte(nil), cstrBytes(r[3:11])...)
			u.Name, u.NameEnc = decodeName(r[11:35])
			u.Card = le.Uint32(r[35:])
			u.UserID = string(cstrBytes(r[48:72]))
		}
		users = append(users, u)
	}
	return users, nil
}

type zkLog struct {
	UserID string
	Time   time.Time
}

func decodeZKTime(t uint32) time.Time {
	s := int(t % 60)
	t /= 60
	mi := int(t % 60)
	t /= 60
	h := int(t % 24)
	t /= 24
	d := int(t%31) + 1
	t /= 31
	mo := int(t%12) + 1
	t /= 12
	return time.Date(int(t)+2000, time.Month(mo), d, h, mi, s, 0, time.Local)
}

func encodeZKTime(d time.Time) uint32 {
	return uint32(((d.Year()%100)*12*31+(int(d.Month())-1)*31+d.Day()-1)*86400 +
		(d.Hour()*60+d.Minute())*60 + d.Second())
}

func (c *zkClient) getAttendance(users []zkUser) ([]zkLog, error) {
	sz, err := c.readSizes()
	if err != nil {
		return nil, err
	}
	if sz.Records == 0 {
		return []zkLog{}, nil
	}
	data, err := c.readWithBuffer(cmdAttLogRrq, 0, 0)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return []zkLog{}, nil
	}
	total := int(le.Uint32(data[0:4]))
	data = data[4:]
	recSize := total / sz.Records
	byUID := map[uint16]string{}
	for _, u := range users {
		byUID[u.UID] = u.UserID
	}
	logs := make([]zkLog, 0, sz.Records)
	switch recSize {
	case 8: // 'HB4sB'
		for len(data) >= 8 {
			uid := le.Uint16(data[0:])
			id, ok := byUID[uid]
			if !ok {
				id = strconv.Itoa(int(uid))
			}
			logs = append(logs, zkLog{UserID: id, Time: decodeZKTime(le.Uint32(data[3:]))})
			data = data[8:]
		}
	case 16: // '<I4sBB2sI'
		for len(data) >= 16 {
			logs = append(logs, zkLog{UserID: strconv.FormatUint(uint64(le.Uint32(data[0:])), 10), Time: decodeZKTime(le.Uint32(data[4:]))})
			data = data[16:]
		}
	default: // 40: '<H24sB4sB8s'
		if recSize < 40 {
			recSize = 40
		}
		codeInit := []byte("\xff255\x00\x00\x00\x00\x00")
		for len(data) >= 40 {
			if len(data) >= len(codeInit) && string(data[:len(codeInit)]) == string(codeInit) {
				data = data[len(codeInit):]
				if len(data) < 40 {
					break
				}
			}
			id := string(cstrBytes(data[2:26]))
			logs = append(logs, zkLog{UserID: id, Time: decodeZKTime(le.Uint32(data[27:]))})
			if len(data) < recSize {
				break
			}
			data = data[recSize:]
		}
	}
	return logs, nil
}

func (c *zkClient) readOption(key string) *string {
	d, err := c.cmd(cmdOptionsRrq, append([]byte(key), 0))
	if err != nil {
		return nil
	}
	s := string(cstrBytes(d))
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			s = s[i+1:]
			break
		}
	}
	s = trimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func (c *zkClient) readTime() (time.Time, error) {
	d, err := c.cmd(cmdGetTime, nil)
	if err != nil {
		return time.Time{}, err
	}
	if len(d) < 4 {
		return time.Time{}, errors.New("رد الوقت غير صالح")
	}
	return decodeZKTime(le.Uint32(d)), nil
}

// أوامر قد لا يرد عليها الجهاز لأنه يطفي أو يعيد التشغيل فوراً
func (c *zkClient) cmdNoWait(cmd uint16) {
	old := c.timeout
	c.timeout = 4 * time.Second
	c.send(cmd, nil)
	c.timeout = old
}

func u32(n uint32) []byte { b := make([]byte, 4); le.PutUint32(b, n); return b }

// مفتاح كلمة سر الاتصال (Comm Key)، منقول من pyzk.make_commkey
func makeCommKey(key uint32, session uint16, ticks byte) []byte {
	var k uint32
	for i := 0; i < 32; i++ {
		if key&(1<<uint(i)) != 0 {
			k = k<<1 | 1
		} else {
			k = k << 1
		}
	}
	k += uint32(session)
	b := u32(k)
	b[0] ^= 'Z'
	b[1] ^= 'K'
	b[2] ^= 'S'
	b[3] ^= 'O'
	s := []byte{b[2], b[3], b[0], b[1]}
	return []byte{s[0] ^ ticks, s[1] ^ ticks, ticks, s[3] ^ ticks}
}
