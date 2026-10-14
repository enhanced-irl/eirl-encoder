package main

import (
	"io/fs"

	"github.com/enhanced-irl/eirl-encoder/manager/web"
)

func webFS() fs.FS { return web.FS() }

func webHash() string { return web.Hash() }
