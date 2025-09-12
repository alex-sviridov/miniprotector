package filesystem

import (
	"bytes"
	"encoding/gob"
	"time"
	"io/fs"
)

// fileInfoGob is a temporary struct with exported fields for gob encoding
type fileInfoGob struct {
	Host          string
	Path          string
	Name          string
	Size          int64
	Mode          fs.FileMode
	Owner         uint32
	Group         uint32
	ModTime       time.Time
	AccessTime    time.Time
	CTime         time.Time
	SymlinkTarget string
	Attributes    []byte
	Acl           []byte
}

// GobEncode implements gob.GobEncoder
func (fi *FileInfo) GobEncode() ([]byte, error) {
	temp := fileInfoGob{
		Host:          fi.host,
		Path:          fi.path,
		Name:          fi.name,
		Size:          fi.size,
		Mode:          fi.mode,
		Owner:         fi.owner,
		Group:         fi.group,
		ModTime:       fi.modTime,
		AccessTime:    fi.accessTime,
		CTime:         fi.cTime,
		SymlinkTarget: fi.symlinkTarget,
		Attributes:    fi.attributes,
		Acl:          fi.acl,
	}
	
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(temp); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GobDecode implements gob.GobDecoder
func (fi *FileInfo) GobDecode(data []byte) error {
	var temp fileInfoGob
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&temp); err != nil {
		return err
	}
	
	fi.host = temp.Host
	fi.path = temp.Path
	fi.name = temp.Name
	fi.size = temp.Size
	fi.mode = temp.Mode
	fi.owner = temp.Owner
	fi.group = temp.Group
	fi.modTime = temp.ModTime
	fi.accessTime = temp.AccessTime
	fi.cTime = temp.CTime
	fi.symlinkTarget = temp.SymlinkTarget
	fi.attributes = temp.Attributes
	fi.acl = temp.Acl
	
	return nil
}

// Encode serializes FileInfo to an efficient gob-encoded string
func (fileInfo *FileInfo) Encode() ([]byte, error) {
	return fileInfo.GobEncode()
}

// DecodeFileInfo deserializes FileInfo from gob-encoded string
func DecodeFileInfo(data []byte) (*FileInfo, error) {
	fileInfo := &FileInfo{}
	if err := fileInfo.GobDecode(data); err != nil {
		return nil, err
	}
	return fileInfo, nil
}
