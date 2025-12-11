#!/bin/bash

# Quick start skript pro Cumulus3 s různými log levely

echo "=== Cumulus3 Log Level Demo ==="
echo ""

# Zastavit běžící instance
docker-compose down 2>/dev/null

echo "1. Spuštění s DEBUG levelem (barevné logy)"
echo "   - Vidíte vše včetně interních detailů"
echo ""
LOG_LEVEL=DEBUG LOG_FORMAT=text LOG_COLOR=true docker-compose up -d
sleep 3

# Testovací upload
echo "   Nahrávám testovací soubor..."
echo "Test file content" > /tmp/test.txt
curl -s -F "file=@/tmp/test.txt" http://localhost:8800/upload > /tmp/upload_result.json
FILE_ID=$(cat /tmp/upload_result.json | grep -o '"file_id":"[^"]*"' | cut -d'"' -f4)
echo "   File ID: $FILE_ID"

echo ""
echo "   Logy:"
docker-compose logs cumulus3 | tail -20
echo ""
read -p "Stiskněte Enter pro pokračování..."

# Restart s INFO levelem
echo ""
echo "2. Restart s INFO levelem (JSON formát pro produkci)"
echo "   - Strukturované logy vhodné pro Grafana Loki"
echo ""
docker-compose down
LOG_LEVEL=INFO LOG_FORMAT=json LOG_COLOR=false docker-compose up -d
sleep 3

# Stažení souboru
echo "   Stahuji testovací soubor..."
curl -s "http://localhost:8800/download?id=$FILE_ID" -o /tmp/downloaded.txt
echo "   Obsah: $(cat /tmp/downloaded.txt)"

echo ""
echo "   JSON logy:"
docker-compose logs cumulus3 | tail -10
echo ""
read -p "Stiskněte Enter pro pokračování..."

# Restart s ERROR levelem
echo ""
echo "3. Restart s ERROR levelem"
echo "   - Pouze chybové zprávy"
echo ""
docker-compose down
LOG_LEVEL=ERROR LOG_FORMAT=json LOG_COLOR=false docker-compose up -d
sleep 3

# Test 404
echo "   Test 404 (neexistující soubor)..."
curl -s "http://localhost:8800/download?id=neexistuje" || true

echo ""
echo "   ERROR logy (mělo by být prázdné, protože 404 není ERROR level):"
docker-compose logs cumulus3 | grep ERROR || echo "   (žádné ERROR logy)"
echo ""

# Ukončení
echo ""
echo "=== Demo dokončeno ==="
echo ""
echo "Pro sledování logů v reálném čase:"
echo "  docker-compose logs -f cumulus3"
echo ""
echo "Pro filtrování kategorií:"
echo "  docker-compose logs cumulus3 | grep UPLOAD"
echo "  docker-compose logs cumulus3 | grep ERROR"
echo ""
echo "Pro parsování JSON logů:"
echo "  docker-compose logs cumulus3 | jq 'select(.level==\"ERROR\")'"
echo ""

# Cleanup
rm -f /tmp/test.txt /tmp/downloaded.txt /tmp/upload_result.json

read -p "Zastavit Cumulus3? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    docker-compose down
    echo "Cumulus3 zastaven."
fi
