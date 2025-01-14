package gobits

import (
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"log"
)

// ServeHTTP handler
func (b *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
        dump, err := httputil.DumpRequest(r, false)
	if err != nil {
		log.Printf("")
		http.Error(w, "Internal Server error", http.StatusInternalServerError)
		return
	}
	log.Printf("Request:\n%s", dump)
	// Only allow BITS requests
	if r.Method != b.cfg.AllowedMethod {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// get packet type and session id
	packetType := strings.ToLower(r.Header.Get("BITS-Packet-Type"))
	sessionID := r.Header.Get("BITS-Session-Id")

	// Take appropriate action based on what type of packet we got
	switch packetType {
	case "ping":
		log.Printf("ping request: %s", r)
		b.bitsPing(w, r)
		return
	case "create-session":
		log.Printf("create-session request: %s", r)
		b.bitsCreate(w, r)
	case "cancel-session":
		log.Printf("cancel-session request: %s", r)
		b.bitsCancel(w, r, sessionID)
	case "close-session":
		log.Printf("close-session request: %s", r)
		b.bitsClose(w, r, sessionID)
	case "fragment":
		log.Printf("fragment request: %s", r)
		b.bitsFragment(w, r, sessionID)
	default:
		log.Printf("error occured: %s", r)
		bitsError(w, "", http.StatusBadRequest, 0, ErrorContextRemoteFile)
	}
}

// use the Ping packet to establish a connection and negotiate security with the server.
// https://msdn.microsoft.com/en-us/library/aa363135(v=vs.85).aspx
func (b *Handler) bitsPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("BITS-Packet-Type", "Ack")
	w.Write(nil)
}

// use the Create-Session packet to request an upload session with the BITS server.
// https://msdn.microsoft.com/en-us/library/aa362833(v=vs.85).aspx
func (b *Handler) bitsCreate(w http.ResponseWriter, r *http.Request) {

	// Check for correct protocol
	var protocol string
	protocols := strings.Split(r.Header.Get("BITS-Supported-Protocols"), " ")
	log.Printf("all protocols from request: %s", protocols)
	for _, protocol = range protocols {
		if protocol == b.cfg.Protocol {
			log.Printf("bitsCreate break taken!")
			break
		}
	}
	log.Printf("configured protocol from config: %s", b.cfg.Protocol)
	log.Printf("protocol from request: %s", protocol)
	if protocol != b.cfg.Protocol {
		// no matching protocol found
		log.Printf("Create-Session: no matching protocol found. %s", r)
		bitsError(w, "", http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// Create new session UUID
	uuid, err := newUUID()
	if err != nil {
		log.Printf("Error creating new session UUID: %s", err.Error())
		bitsError(w, "", http.StatusInternalServerError, 0, ErrorContextRemoteFile)
		return
	}
	log.Printf("New SessionID: %s", uuid)

	// Create session directory
	tmpDir := path.Join(b.cfg.TempDir, uuid)
	if err = os.MkdirAll(tmpDir, 0755); err != nil {
		log.Printf("error mkdirAll: %s", err.Error())
		bitsError(w, "", http.StatusInternalServerError, 0, ErrorContextRemoteFile)
		return
	}
	log.Printf("tmpDir '%s' have been created", tmpDir)

	// make sure we actually have a callback before calling it
	if b.callback != nil {
		b.callback(EventCreateSession, uuid, tmpDir)
	}

	// https://msdn.microsoft.com/en-us/library/aa362771(v=vs.85).aspx
	w.Header().Add("BITS-Packet-Type", "Ack")
	w.Header().Add("BITS-Protocol", protocol)
	w.Header().Add("BITS-Session-Id", uuid)
	w.Header().Add("Accept-Encoding", "Identity")
	w.Write(nil)

}

// Use the Fragment packet to send a fragment of the upload file to the server
// https://msdn.microsoft.com/en-us/library/aa362842(v=vs.85).aspx
func (b *Handler) bitsFragment(w http.ResponseWriter, r *http.Request, uuid string) {

	// Check for correct session
	if uuid == "" || !isValidUUID(uuid) {
		log.Printf("session UUID ('%s') is empty or invalid", uuid)
		bitsError(w, "", http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// Check for existing session
	var srcDir string
	srcDir = path.Join(b.cfg.TempDir, uuid)
	if b, _ := exists(srcDir); !b {
		log.Printf("srcDir does not exist")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	// Create session directory
	//tmpDir := path.Join(b.cfg.TempDir, uuid)
	var err error
	if err = os.MkdirAll(srcDir, 0755); err != nil {
		log.Printf("error mkdirAll: %s", err.Error())
		bitsError(w, "", http.StatusInternalServerError, 0, ErrorContextRemoteFile)
		return
	}
	log.Printf("srcDir '%s' have been created", srcDir)

	// Get filename and make sure the path is correct
	_, filename := path.Split(r.RequestURI)
	if filename == "" {
		log.Printf("path is not correct")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	//var err error
	var match bool

	// See if filename is blacklisted. If so, return an error
	for _, reg := range b.cfg.Disallowed {
		match, err = regexp.MatchString(reg, filename)
		if err != nil {
			log.Printf("error matching disallowed filename")
			bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
			return
		}
		if match {
			// File is blacklisted
			log.Printf("filename ('%s') is blacklisted", filename)
			bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
			return
		}
	}

	// See if filename is whitelisted
	allowed := false
	for _, reg := range b.cfg.Allowed {
		match, err = regexp.MatchString(reg, filename)
		if err != nil {
			log.Printf("error matching allowed filename")
			bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
			return
		}
		if match {
			allowed = true
			break
		}
	}
	if !allowed {
		// No whitelisting rules matched!
		log.Printf("filename ('%s') is not whitelisted", filename)
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	var src string

	// Get absolute paths to file
	src, err = filepath.Abs(filepath.Join(srcDir, filename))
	if err != nil {
		src = filepath.Join(srcDir, filename)
	}

	// Parse range
	var rangeStart, rangeEnd, fileLength uint64
	rangeStart, rangeEnd, fileLength, err = parseRange(r.Header.Get("Content-Range"))
	if err != nil {
		log.Printf("error parsing range: %s", err.Error())
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// Check filesize
	if b.cfg.MaxSize > 0 && fileLength > b.cfg.MaxSize {
		log.Printf("file is too big, max allowed size is: %s", b.cfg.MaxSize)
		bitsError(w, uuid, http.StatusRequestEntityTooLarge, 0, ErrorContextRemoteFile)
		return
	}

	// Get the length of the posted data
	var fragmentSize uint64
	fragmentSize, err = strconv.ParseUint(r.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		log.Printf("error parsing fragmentSize")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// Get posted data and confirm size
	data, err := ioutil.ReadAll(r.Body) // should probably not read everything into memory like this
	if err != nil {
		log.Printf("error reading data: %s", err.Error())
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	if uint64(len(data)) != fragmentSize {
		log.Printf("error: size of data is not equal to fragmentSize")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// Check that content-range size matches content-length
	if rangeEnd-rangeStart+1 != fragmentSize {
		log.Printf("error: range size does not match fragmentSize")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// Open or create file
	var file *os.File
	var fileSize uint64
	var exist bool
	exist, err = exists(src)
	if err != nil {
		log.Printf("error: src file exists: %s", err.Error())
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	if exist != true {
		// Create file
		file, err = os.OpenFile(src, os.O_CREATE|os.O_WRONLY, 0755)
		if err != nil {
			log.Printf("error creating new file ('%s'): %s", src, err.Error())
			bitsError(w, uuid, http.StatusInternalServerError, 0, ErrorContextRemoteFile)
			return
		}
		defer file.Close()

		// New file, size is zero
		fileSize = 0

	} else {
		// Open file for append
		file, err = os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0755)
		if err != nil {
			log.Printf("error appending to file ('%s'): %s", src, err.Error())
			bitsError(w, uuid, http.StatusInternalServerError, 0, ErrorContextRemoteFile)
			return
		}
		defer file.Close()

		// Get size on disk
		var info os.FileInfo
		info, err = file.Stat()
		if err != nil {
			log.Printf("error getting file stat: %s", err.Error())
			bitsError(w, uuid, http.StatusInternalServerError, 0, ErrorContextRemoteFile)
			return
		}
		fileSize = uint64(info.Size())

	}

	// Sanity checks
	if rangeEnd < fileSize {
		// The range is already written to disk
		log.Printf("range already written to disk")
		w.Header().Add("BITS-Recieved-Content-Range", strconv.FormatUint(fileSize, 10))
		bitsError(w, uuid, http.StatusRequestedRangeNotSatisfiable, 0, ErrorContextRemoteFile)
		return
	} else if rangeStart > fileSize {
		// start must be <= fileSize, else there will be a gap
		log.Printf("gap in file detected")
		w.Header().Add("BITS-Recieved-Content-Range", strconv.FormatUint(fileSize, 10))
		bitsError(w, uuid, http.StatusRequestedRangeNotSatisfiable, 0, ErrorContextRemoteFile)
		return
	}

	// Calculate the offset in the slice, if overlapping
	var dataOffset = fileSize - rangeStart

	// Write the data to file
	var written uint64
	var wr int
	wr, err = file.Write(data[dataOffset:])
	if err != nil {
		log.Printf("error writing file: %s", err.Error())
		bitsError(w, uuid, http.StatusInternalServerError, 0, ErrorContextRemoteFile)
		return
	}
	written = uint64(wr)
	log.Printf("%d bytes written", written)

	// Make sure we wrote everything we wanted
	if written != fragmentSize-dataOffset {
		log.Printf("writing less data than expected")
		bitsError(w, uuid, http.StatusInternalServerError, 0, ErrorContextRemoteFile)
		return
	}

	// Check if we have written everything
	if rangeEnd+1 == fileLength {
		// File is done! Manually close it, since the callback probably don't wnat the file to be open
		file.Close()

		// Call the callback
		if b.callback != nil {
			b.callback(EventRecieveFile, uuid, src)
		}

	}

	// https://msdn.microsoft.com/en-us/library/aa362773(v=vs.85).aspx
	w.Header().Add("BITS-Packet-Type", "Ack")
	w.Header().Add("BITS-Session-Id", uuid)
	w.Header().Add("BITS-Received-Content-Range", strconv.FormatUint(fileSize+uint64(written), 10))
	w.Write(nil)

}

// Use the Cancel-Session packet to terminate the upload session with the BITS server.
// https://msdn.microsoft.com/en-us/library/aa362829(v=vs.85).aspx
func (b *Handler) bitsCancel(w http.ResponseWriter, r *http.Request, uuid string) {
	// Check for correct session
	if uuid == "" || !isValidUUID(uuid) {
		log.Printf("bitsCancel error, uuid ('%s') empty or not valid", uuid)
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	destDir := path.Join(b.cfg.TempDir, uuid)
	exist, err := exists(destDir)
	if err != nil {
		log.Printf("error checking if dstDir already exists: %s", err.Error())
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	if !exist {
		log.Printf("dstDir does not exist")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// do the callback
	if b.callback != nil {
		b.callback(EventCancelSession, uuid, destDir)
	}

	w.Header().Add("BITS-Packet-Type", "Ack")
	w.Header().Add("BITS-Session-Id", uuid)
	w.Write(nil)
}

// Use the Close-Session packet to tell the BITS server that file upload is complete and to end the session.
// https://msdn.microsoft.com/en-us/library/aa362830(v=vs.85).aspx
func (b *Handler) bitsClose(w http.ResponseWriter, r *http.Request, uuid string) {
	// Check for correct session
	if uuid == "" || !isValidUUID(uuid) {
		log.Printf("bitsClose error, uuid ('%s') empty or not valid", uuid)
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	destDir := path.Join(b.cfg.TempDir, uuid)
	exist, err := exists(destDir)
	if err != nil {
		log.Printf("error checking if dstDir already exists: %s", err.Error())
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}
	if !exist {
		log.Printf("dstDir does not exist")
		bitsError(w, uuid, http.StatusBadRequest, 0, ErrorContextRemoteFile)
		return
	}

	// do the callback
	if b.callback != nil {
		b.callback(EventCloseSession, uuid, destDir)
	}

	// https://msdn.microsoft.com/en-us/library/aa362712(v=vs.85).aspx
	w.Header().Add("BITS-Packet-Type", "Ack")
	w.Header().Add("BITS-Session-Id", uuid)
	w.Write(nil)
}
