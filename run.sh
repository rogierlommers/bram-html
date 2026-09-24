#!/bin/bash
echo "sourcing .env file"
source .env

echo "running backend"
go run ./backend
