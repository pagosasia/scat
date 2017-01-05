package aprocs

import (
	"fmt"
	"os"
	"os/exec"

	ss "secsplit"
	"secsplit/tmpdedup"
)

type pathCmdIn struct {
	newCmd PathCmdInFn
	tmp    *tmpdedup.Dir
}

type PathCmdInFn func(*ss.Chunk, string) (*exec.Cmd, error)

func NewPathCmdIn(newCmd PathCmdInFn, tmp *tmpdedup.Dir) Proc {
	cmdp := pathCmdIn{
		newCmd: newCmd,
		tmp:    tmp,
	}
	return InplaceProcFunc(cmdp.process)
}

func (cmdp *pathCmdIn) process(c *ss.Chunk) (err error) {
	filename := fmt.Sprintf("%x", c.Hash)
	path, wg, err := cmdp.tmp.Get(filename, func(path string) (err error) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return
		}
		defer f.Close()
		_, err = f.Write(c.Data)
		return
	})
	if err != nil {
		return
	}
	defer wg.Done()
	cmd, err := cmdp.newCmd(c, path)
	if err != nil {
		return
	}
	return cmd.Run()
}
