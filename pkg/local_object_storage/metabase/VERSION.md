# Metabase versioning

This file describes changes between the metabase versions.

## Current

Numbers stand for a single byte value.

### Primary buckets
- Graveyard bucket
  - Name: `0`
  - Key: object address 
  - Value: tombstone address
- Garbage objects bucket
  - Name: `1`
  - Key: object address
  - Value: dummy value
- Garbage containers bucket
  - Name: `17`
  - Key: container ID
  - Value: dummy value
- Bucket containing IDs of objects that are candidates for moving
   to another shard.
  - Name: `2`
  - Key: object address
  - Value: dummy value
- Container volume bucket
  - Name: `3`
  - Key: container ID
  - Value: container size in bytes as little-endian uint64
- Bucket for storing locked objects information
  - Name: `4` 
  - Key: container ID
  - Value: bucket mapping objects locked to the list of corresponding LOCK objects
- Bucket containing auxilliary information. All keys are custom and are not connected to the container
  - Name: `5`
  - Keys and values
    - `id` -> shard id as bytes
    - `version` -> metabase version as little-endian uint64
    - `phy_counter` -> shard's physical object counter as little-endian uint64
    - `logic_counter` -> shard's logical object counter as little-endian uint64

### Unique index buckets
- Buckets containing objects of REGULAR type
  - Name: container ID
  - Key: object ID
  - Value: marshalled object
- Buckets containing objects of LOCK type
  - Name: container ID + `7`
  - Key: object ID
  - Value: marshalled object
- Buckets containing objects of STORAGEGROUP type
  - Name: container ID + 8
  - Key: object ID
  - Value: marshaled object
- Buckets containing objects of TOMBSTONE type
  - Name: container ID + `9`
  - Key: object ID
  - Value: marshaled object
- Buckets mapping objects to the storage ID they are stored in
  - Name: container ID + `10`
  - Key: object ID
  - Value: storage ID
- Buckets for mapping parent object to the split info
  - Name: container ID + `11`
  - Key: object ID
  - Value: split info

### FKBT index buckets
- Buckets mapping owner to object IDs
  - Name: containerID + `12`
  - Key: owner ID as base58 string
  - Value: bucket containing object IDs as keys
- Buckets containing objects attributes indexes
  - Name: containerID + `13` + attribute key
  - Key: attribute value
  - Value: bucket containing object IDs as keys

### List index buckets
- Buckets mapping payload hash to a list of object IDs
  - Name: container ID + `14`
  - Key: payload hash
  - Value: list of object IDs
- Buckets mapping parent ID to a list of children IDs
  - Name: container ID + `15`
  - Key: parent ID
  - Value: list of children object IDs
- Buckets mapping split ID to a list of object IDs
  - Name: container ID + `16`
  - Key: split ID
  - Value: list of object IDs

# History

## Version 2

- Container ID is encoded as 32-byte slice
- Object ID is encoded as 32-byte slice
- Object ID is encoded as 64-byte slice, container ID + object ID
- Bucket naming scheme is changed:
  - container ID + suffix -> 1-byte prefix + container ID

## Version 1

- Metabase now stores generic storage id instead of blobovnicza ID.

## Version 0

- Container ID is encoded as base58 string
- Object ID is encoded as base58 string
- Address is encoded as container ID + "/" + object ID