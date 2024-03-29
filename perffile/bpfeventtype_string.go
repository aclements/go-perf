// Code generated by "bitstringer -type=BPFEventType"; DO NOT EDIT

package perffile

import "strconv"

func (i BPFEventType) String() string {
	if i == 0 {
		return "Unknown"
	}
	s := ""
	if i&BPFEventTypeProgLoad != 0 {
		s += "ProgLoad|"
	}
	if i&BPFEventTypeProgUnload != 0 {
		s += "ProgUnload|"
	}
	i &^= 3
	if i == 0 {
		return s[:len(s)-1]
	}
	return s + "0x" + strconv.FormatUint(uint64(i), 16)
}
