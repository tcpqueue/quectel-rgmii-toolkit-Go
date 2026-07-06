package main

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

type smsPDUOutgoing struct {
	PDUHex     string
	TPDUOctets int
}

type smsPDUDecoded struct {
	ServiceCenter string
	Sender        string
	Date          string
	Text          string
	ConcatRef     string
	ConcatTotal   int
	ConcatSeq     int
}

func buildSMSSubmitPDUs(number string, message string, ref int) []smsPDUOutgoing {
	segments := splitSMSRunes(message, 67)
	if len(segments) == 0 {
		return nil
	}
	if ref <= 0 {
		ref = 1
	}
	out := make([]smsPDUOutgoing, 0, len(segments))
	digits := stripNonDigits(number)
	if digits == "" {
		return nil
	}
	toa := byte(0x81)
	if strings.HasPrefix(strings.TrimSpace(number), "+") {
		toa = 0x91
	}
	encodedNumber := encodePDUPhoneNumber(digits)
	for i, segment := range segments {
		firstOctet := byte(0x11)
		userData := encodeUCS2(segment)
		if len(segments) > 1 {
			firstOctet = 0x51
			udh := fmt.Sprintf("050003%02X%02X%02X", ref&0xFF, len(segments), i+1)
			userData = udh + userData
		}
		udOctets := len(userData) / 2
		tpdu := fmt.Sprintf("%02X00%02X%02X%s0008AA%02X%s", firstOctet, len(digits), toa, encodedNumber, udOctets, userData)
		out = append(out, smsPDUOutgoing{PDUHex: "00" + tpdu, TPDUOctets: len(tpdu) / 2})
	}
	return out
}

func parseSMSDeliverPDU(pdu string) (smsPDUDecoded, bool) {
	data, ok := cleanHexBytes(pdu)
	if !ok || len(data) < 2 {
		return smsPDUDecoded{}, false
	}
	pos := 0
	smscLen := int(data[pos])
	pos++
	decoded := smsPDUDecoded{}
	if smscLen > 0 {
		if pos+smscLen > len(data) || smscLen < 1 {
			return smsPDUDecoded{}, false
		}
		toa := data[pos]
		raw := data[pos+1 : pos+smscLen]
		decoded.ServiceCenter = decodePDUAddress((smscLen-1)*2, toa, raw)
		pos += smscLen
	}
	if pos >= len(data) {
		return smsPDUDecoded{}, false
	}
	firstOctet := data[pos]
	pos++
	if firstOctet&0x03 != 0x00 {
		return smsPDUDecoded{}, false
	}
	if pos+2 > len(data) {
		return smsPDUDecoded{}, false
	}
	addrDigits := int(data[pos])
	pos++
	addrTOA := data[pos]
	pos++
	addrOctets := (addrDigits + 1) / 2
	if pos+addrOctets+10 > len(data) {
		return smsPDUDecoded{}, false
	}
	decoded.Sender = decodePDUAddress(addrDigits, addrTOA, data[pos:pos+addrOctets])
	pos += addrOctets
	pos++ // PID
	dcs := data[pos]
	pos++
	decoded.Date = decodePDUTimestamp(data[pos : pos+7])
	pos += 7
	udl := int(data[pos])
	pos++
	if pos > len(data) {
		return smsPDUDecoded{}, false
	}
	userData := data[pos:]
	if len(userData) == 0 && udl > 0 {
		return smsPDUDecoded{}, false
	}
	decoded.Text, decoded.ConcatRef, decoded.ConcatTotal, decoded.ConcatSeq = decodePDUUserData(firstOctet, dcs, udl, userData)
	return decoded, true
}

func decodePDUUserData(firstOctet byte, dcs byte, udl int, userData []byte) (string, string, int, int) {
	udhi := firstOctet&0x40 != 0
	content := userData
	udhSeptets := 0
	concatRef := ""
	concatTotal := 0
	concatSeq := 0
	if udhi && len(userData) > 0 {
		udhl := int(userData[0])
		if len(userData) >= udhl+1 {
			udh := userData[1 : udhl+1]
			concatRef, concatTotal, concatSeq = parseSMSConcatUDH(udh)
			content = userData[udhl+1:]
			udhSeptets = ((udhl+1)*8 + 6) / 7
		}
	}
	switch smsDCSAlphabet(dcs) {
	case "ucs2":
		return decodeUCS2Bytes(content), concatRef, concatTotal, concatSeq
	case "gsm7":
		septets := udl
		if udhi {
			septets -= udhSeptets
			if septets < 0 {
				septets = 0
			}
			return decodeGSM7(userData, septets, udhSeptets), concatRef, concatTotal, concatSeq
		}
		return decodeGSM7(content, septets, 0), concatRef, concatTotal, concatSeq
	case "8bit":
		return strings.ToUpper(hex.EncodeToString(content)), concatRef, concatTotal, concatSeq
	default:
		return decodeMaybeUCS2(strings.ToUpper(hex.EncodeToString(content))), concatRef, concatTotal, concatSeq
	}
}

func parseSMSConcatUDH(udh []byte) (string, int, int) {
	for i := 0; i+1 < len(udh); {
		iei := udh[i]
		l := int(udh[i+1])
		i += 2
		if i+l > len(udh) {
			break
		}
		data := udh[i : i+l]
		switch {
		case iei == 0x00 && l == 3:
			return fmt.Sprintf("8:%02X", data[0]), int(data[1]), int(data[2])
		case iei == 0x08 && l == 4:
			return fmt.Sprintf("16:%02X%02X", data[0], data[1]), int(data[2]), int(data[3])
		}
		i += l
	}
	return "", 0, 0
}

func smsDCSAlphabet(dcs byte) string {
	switch dcs & 0x0C {
	case 0x08:
		return "ucs2"
	case 0x04:
		return "8bit"
	default:
		return "gsm7"
	}
}

func encodeUCS2(value string) string {
	units := utf16.Encode([]rune(value))
	var b strings.Builder
	for _, unit := range units {
		fmt.Fprintf(&b, "%04X", unit)
	}
	return b.String()
}

func decodeUCS2Bytes(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
	}
	return string(utf16.Decode(units))
}

func encodePDUPhoneNumber(digits string) string {
	if len(digits)%2 != 0 {
		digits += "F"
	}
	var b strings.Builder
	for i := 0; i+1 < len(digits); i += 2 {
		b.WriteByte(digits[i+1])
		b.WriteByte(digits[i])
	}
	return strings.ToUpper(b.String())
}

func decodePDUAddress(digitCount int, toa byte, raw []byte) string {
	digits := decodePDUSemiOctets(raw, digitCount)
	if digits == "" {
		return ""
	}
	if toa&0x90 == 0x90 && !strings.HasPrefix(digits, "+") {
		return "+" + digits
	}
	return digits
}

func decodePDUSemiOctets(raw []byte, digitCount int) string {
	var b strings.Builder
	for _, value := range raw {
		lo := value & 0x0F
		hi := (value >> 4) & 0x0F
		if lo <= 9 {
			b.WriteByte(byte('0' + lo))
		}
		if hi <= 9 {
			b.WriteByte(byte('0' + hi))
		}
	}
	out := b.String()
	if digitCount > 0 && len(out) > digitCount {
		out = out[:digitCount]
	}
	return out
}

func decodePDUTimestamp(data []byte) string {
	if len(data) < 7 {
		return ""
	}
	parts := make([]int, 6)
	for i := 0; i < 6; i++ {
		parts[i] = pduSemiOctetInt(data[i])
	}
	tzByte := data[6]
	sign := "+"
	if tzByte&0x08 != 0 {
		sign = "-"
		tzByte &^= 0x08
	}
	tz := pduSemiOctetInt(tzByte)
	return fmt.Sprintf("%02d/%02d/%02d,%02d:%02d:%02d%s%02d", parts[0], parts[1], parts[2], parts[3], parts[4], parts[5], sign, tz)
}

func pduSemiOctetInt(value byte) int {
	return int(value&0x0F)*10 + int((value>>4)&0x0F)
}

func cleanHexBytes(value string) ([]byte, bool) {
	clean := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	if clean == "" || len(clean)%2 != 0 {
		return nil, false
	}
	data, err := hex.DecodeString(clean)
	if err != nil {
		return nil, false
	}
	return data, true
}

func isHexLine(value string) bool {
	clean := strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	if clean == "" || len(clean)%2 != 0 {
		return false
	}
	_, err := strconv.ParseUint(clean[:2], 16, 8)
	if err != nil {
		return false
	}
	_, err = hex.DecodeString(clean)
	return err == nil
}

func decodeGSM7(data []byte, septets int, skipSeptets int) string {
	if septets <= 0 || len(data) == 0 {
		return ""
	}
	var b strings.Builder
	escape := false
	for i := 0; i < septets; i++ {
		bit := (skipSeptets + i) * 7
		value := 0
		for j := 0; j < 7; j++ {
			idx := (bit + j) / 8
			if idx >= len(data) {
				break
			}
			if data[idx]&(1<<uint((bit+j)%8)) != 0 {
				value |= 1 << uint(j)
			}
		}
		if escape {
			b.WriteRune(gsm7ExtRune(byte(value)))
			escape = false
			continue
		}
		if value == 0x1B {
			escape = true
			continue
		}
		b.WriteRune(gsm7Rune(byte(value)))
	}
	return b.String()
}

func gsm7Rune(value byte) rune {
	table := []rune{
		'@', '£', '$', '¥', 'è', 'é', 'ù', 'ì', 'ò', 'Ç', '\n', 'Ø', 'ø', '\r', 'Å', 'å',
		'Δ', '_', 'Φ', 'Γ', 'Λ', 'Ω', 'Π', 'Ψ', 'Σ', 'Θ', 'Ξ', 0, 'Æ', 'æ', 'ß', 'É',
		' ', '!', '"', '#', '¤', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
		'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':', ';', '<', '=', '>', '?',
		'¡', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O',
		'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§',
		'¿', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o',
		'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'ä', 'ö', 'ñ', 'ü', 'à',
	}
	if int(value) >= len(table) || table[value] == 0 {
		return ' '
	}
	return table[value]
}

func gsm7ExtRune(value byte) rune {
	switch value {
	case 0x0A:
		return '\f'
	case 0x14:
		return '^'
	case 0x28:
		return '{'
	case 0x29:
		return '}'
	case 0x2F:
		return '\\'
	case 0x3C:
		return '['
	case 0x3D:
		return '~'
	case 0x3E:
		return ']'
	case 0x40:
		return '|'
	case 0x65:
		return '€'
	default:
		return ' '
	}
}

func buildMockSMSDeliverPDU(sender string, text string) string {
	digits := stripNonDigits(sender)
	toa := byte(0x81)
	if strings.HasPrefix(strings.TrimSpace(sender), "+") {
		toa = 0x91
	}
	ud := encodeUCS2(text)
	tpdu := fmt.Sprintf("04%02X%02X%s000862502320000000%02X%s", len(digits), toa, encodePDUPhoneNumber(digits), len(ud)/2, ud)
	return "00" + tpdu
}
