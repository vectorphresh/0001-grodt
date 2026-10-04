package mcp

import "encoding/json"

// Validate the accepted tools-only result envelope without interpreting domain
// content, following resource links, or discarding supported content blocks.
var resultSchema = json.RawMessage(`{
 "type":"object", "required":["content"],
 "properties":{
  "isError":{"type":"boolean"},
  "structuredContent":{},
  "content":{"type":"array","items":{"oneOf":[
   {"type":"object","required":["type","text"],"properties":{"type":{"const":"text"},"text":{"type":"string"}}},
   {"type":"object","required":["type","data","mimeType"],"properties":{"type":{"enum":["image","audio"]},"data":{"type":"string"},"mimeType":{"type":"string"}}},
   {"type":"object","required":["type","name","uri"],"properties":{"type":{"const":"resource_link"},"name":{"type":"string"},"uri":{"type":"string"}}},
   {"type":"object","required":["type","resource"],"properties":{"type":{"const":"resource"},"resource":{"type":"object","required":["uri"],"properties":{"uri":{"type":"string"},"text":{"type":"string"},"blob":{"type":"string"}},"oneOf":[{"required":["text"]},{"required":["blob"]}]}}}
  ]}}
 }
}`)
