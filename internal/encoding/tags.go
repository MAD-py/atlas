package encoding

const (
	tagNull      byte = 0x00
	tagFalse     byte = 0x01
	tagTrue      byte = 0x02
	tagInt       byte = 0x03
	tagFloat64   byte = 0x04
	tagString    byte = 0x05
	tagArray     byte = 0x06
	tagObject    byte = 0x07
	tagDate      byte = 0x08
	tagTimestamp byte = 0x09
)
