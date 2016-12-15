package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"os"
)

type SwfHeader struct {
	Signature  [3]uint8
	Version    uint8
	FileLength uint32
	// compressed after this point
}

const (
	TAG_TELEMETRY       uint16 = 93
	TAG_SIGNED_SWF      uint16 = 94
	TAG_FILE_ATTRIBUTES uint16 = 69
	TAG_META            uint16 = 77
	TAG_END             uint16 = 0
	LONG_TAG_LENGTH     uint32 = 63
)

const (
	NO_COMPRESSION   = "FWS"
	ZLIB_COMPRESSION = "CWS"
	LZMA_COMPRESSION = "ZWS"
)

func peekTag(reader io.Reader) (uint16, []uint8) {
	var tagBuffer *bytes.Buffer = new(bytes.Buffer)

	// Tag can either be a short tag (uint16), or long tag (uint16)(uint32)
	// if lower 6 bits are 0x3F, we read 4 more bytes because it's a long tag.
	// upper 10 bits: type
	// lower 6 bits:  tag length
	// Note: 0x3F is just tagLength = 63
	var tagCodeAndLength uint16
	binary.Read(reader, binary.LittleEndian, &tagCodeAndLength)
	binary.Write(tagBuffer, binary.LittleEndian, &tagCodeAndLength)

	var tagType uint16 = tagCodeAndLength >> 6
	var tagLength uint32 = uint32(tagCodeAndLength) & 0x3f

	// a long tag, need more data
	if tagLength >= LONG_TAG_LENGTH {
		var longLength uint32
		binary.Read(reader, binary.LittleEndian, &longLength)
		binary.Write(tagBuffer, binary.LittleEndian, &longLength)
		tagLength = longLength
	}
	tagData := make([]byte, tagLength)
	io.ReadFull(reader, tagData)
	tagBuffer.Write(tagData)

	return tagType, tagBuffer.Bytes()
}

func writeTelemetryTag(writer io.Writer, headerSize *uint32) {
	// if we're a short tag, we're only 2 bytes (uint16)
	var tagLength uint16 = 2

	var tagCodeAndLength uint16 = TAG_TELEMETRY
	if uint32(tagLength) >= LONG_TAG_LENGTH {
		tagCodeAndLength = tagCodeAndLength<<6 | 0x3f
		binary.Write(writer, binary.LittleEndian, &tagCodeAndLength)
		binary.Write(writer, binary.LittleEndian, &tagLength)
		*headerSize += 2
		*headerSize += 4
	} else {
		tagCodeAndLength = tagCodeAndLength<<6 | tagLength
		binary.Write(writer, binary.LittleEndian, &tagCodeAndLength)
		*headerSize += 2
	}

	var reservedPadding uint16 = 0
	binary.Write(writer, binary.LittleEndian, &reservedPadding)
	*headerSize += 2
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("You must supply a path as the first argument!")
		return
	}

	pathToSwf := os.Args[1]
	swfFile, err := os.Open(pathToSwf)
	defer swfFile.Close()

	if err != nil {
		fmt.Println(err)
		return
	}

	var swfHeader SwfHeader
	err = binary.Read(swfFile, binary.LittleEndian, &swfHeader)
	if err != nil {
		fmt.Println(err)
		return
	}

	outFile, err := ioutil.TempFile("", "tele")
	if err != nil {
		fmt.Println(err)
		return
	}
	
	defer outFile.Close()

	compressionType := string(swfHeader.Signature[:])

	var reader io.Reader
	var writer io.WriteCloser

	if compressionType == NO_COMPRESSION {
		reader = swfFile
		writer = outFile
	} else if compressionType == ZLIB_COMPRESSION {
		reader, err = zlib.NewReader(swfFile)
		if err != nil {
			fmt.Println(err)
			return
		}
		writer = zlib.NewWriter(outFile)
	} else if compressionType == LZMA_COMPRESSION {
		fmt.Println("Not supported yet")
		return
	}

	// We need to keep track of our uncompressed length because we are
	// (potentially) streaming into a compressed file and thus we lose the real length
	var headerUncompressedLength uint32 = 0

	// The first part of the header is not compressed
	binary.Write(outFile, binary.LittleEndian, &swfHeader)
	headerUncompressedLength += 8

	// The RECT structure looks like so:
	// UB = unsigned-bit
	// SB = signed-bit

	// NBits UB[5]
	// Xmin  SB[NBits]
	// XMax  SB[NBits]
	// Ymin  SB[NBits]
	// YMax  SB[NBits]
	var frameSize uint8
	binary.Read(reader, binary.LittleEndian, &frameSize)

	// NBits is stored in 5 bits at the start of the structure. Shift it over 3 bits to get a proper byte.
	var nBits uint8 = (frameSize & 0xff) >> 3

	// Figure out how many bytes we need based on the size of nBits.
	var numberOfBytes uint32 = (7 + (uint32(nBits) * 4) - 3) / 8
	frameData := make([]byte, numberOfBytes)
	io.ReadFull(reader, frameData)

	binary.Write(writer, binary.LittleEndian, &frameSize)
	binary.Write(writer, binary.LittleEndian, &frameData)
	headerUncompressedLength += 1
	headerUncompressedLength += numberOfBytes

	var frameRate uint16
	var frameCount uint16
	binary.Read(reader, binary.LittleEndian, &frameRate)
	binary.Read(reader, binary.LittleEndian, &frameCount)
	binary.Write(writer, binary.LittleEndian, &frameRate)
	binary.Write(writer, binary.LittleEndian, &frameCount)
	headerUncompressedLength += 2
	headerUncompressedLength += 2

	for {
		tagType, tagData := peekTag(reader)
		if tagType == TAG_TELEMETRY {
			panic("Bad SWF: Telemetry is already enabled on this SWF")
		} else if tagType == TAG_SIGNED_SWF {
			panic("Bad SWF: Signed SWFs are not supported")
		} else if tagType == TAG_FILE_ATTRIBUTES {
			writer.Write(tagData)
			headerUncompressedLength += uint32(len(tagData))

			nextTagType, nextTagData := peekTag(reader)
			if nextTagType == TAG_META {
				writer.Write(nextTagData)
				headerUncompressedLength += uint32(len(nextTagData))
			}
			writeTelemetryTag(writer, &headerUncompressedLength)
			if nextTagType != TAG_META {
				writer.Write(nextTagData)
				headerUncompressedLength += uint32(len(nextTagData))
			}
		} else if tagType == TAG_END {
			writer.Write(tagData)
			headerUncompressedLength += uint32(len(tagData))
			break
		} else {
			writer.Write(tagData)
			headerUncompressedLength += uint32(len(tagData))
		}
	}
	if compressionType != NO_COMPRESSION {
		writer.Close()
	}

	// write out the total uncompressed file size length
	_, err = outFile.Seek(4, os.SEEK_SET)
	if err != nil {
		fmt.Println(err)
		return
	}
	binary.Write(outFile, binary.LittleEndian, headerUncompressedLength)

	// Permissions of the original file
	swfFileInfo, err := swfFile.Stat()
	if err != nil {
		fmt.Println(err)
		return
	}
	
	err = outFile.Chmod(swfFileInfo.Mode())
	if err != nil {
		fmt.Println(err)
		return
	}
	
	err = os.Rename(outFile.Name(), pathToSwf)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Done, final uncompressed length is:")
	fmt.Println(headerUncompressedLength)
}

