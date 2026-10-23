import Foundation
import CoreBluetooth

let serviceID = CBUUID(string: "c9e0b001-49f0-4c98-9c83-3ad797a40d10")
let identityID = CBUUID(string: "c9e0b002-49f0-4c98-9c83-3ad797a40d10")
let rxID = CBUUID(string: "c9e0b003-49f0-4c98-9c83-3ad797a40d10")
let txID = CBUUID(string: "c9e0b004-49f0-4c98-9c83-3ad797a40d10")
func emit(_ value: [String:Any]) {
    if let data = try? JSONSerialization.data(withJSONObject: value) {
        FileHandle.standardOutput.write(data); FileHandle.standardOutput.write(Data([10]))
    }
}
func fail(_ text: String) -> Never { emit(["event":"error", "error":text]); exit(1) }
let mode = CommandLine.arguments[1]
let target = CommandLine.arguments.count > 2 ? CommandLine.arguments[2] : ""
let nickname = CommandLine.arguments.count > 3 ? CommandLine.arguments[3] : ""

final class Adapter: NSObject, CBCentralManagerDelegate, CBPeripheralDelegate, CBPeripheralManagerDelegate {
    var central: CBCentralManager!
    var manager: CBPeripheralManager!
    var current: CBPeripheral?
    var pending: [CBPeripheral] = []
    var seen = Set<UUID>()
    var rx: CBCharacteristic?
    var tx: CBCharacteristic?
    var value = Data(repeating: 0, count: 8)
    var identity = Data()
    var owner: UUID?
    var connected = false
    override init() {
        super.init()
        if mode == "listen" || mode == "pair-listen" {
            identity = try! JSONSerialization.data(withJSONObject: ["id":target,"name":nickname,"pairing":mode == "pair-listen"])
            manager = CBPeripheralManager(delegate:self, queue: .main)
        } else { central = CBCentralManager(delegate:self, queue: .main) }
    }
    func check(_ state: CBManagerState) {
        switch state {
        case .poweredOff: fail("Bluetooth is off; enable it in System Settings")
        case .unauthorized: fail("Bluetooth permission denied; allow your terminal/btfling in Privacy & Security > Bluetooth")
        case .unsupported: fail("Bluetooth LE is unsupported on this device")
        default: break
        }
    }
    func centralManagerDidUpdateState(_ c: CBCentralManager) {
        check(c.state)
        if c.state == .poweredOn { c.scanForPeripherals(withServices:[serviceID], options:[CBCentralManagerScanOptionAllowDuplicatesKey:true]) }
    }
    func centralManager(_ c: CBCentralManager, didDiscover p: CBPeripheral, advertisementData: [String:Any], rssi RSSI: NSNumber) {
        if seen.insert(p.identifier).inserted { pending.append(p); next() }
    }
    func next() {
        if current != nil || pending.isEmpty { return }
        let p = pending.removeFirst(); current = p; p.delegate = self; central.connect(p)
        DispatchQueue.main.asyncAfter(deadline:.now()+10) { [weak self, weak p] in
            guard let self=self, let p=p else{return}
            if self.current === p && !self.connected { self.central.cancelPeripheralConnection(p) }
        }
    }
    func centralManager(_ c: CBCentralManager, didConnect p: CBPeripheral) { p.discoverServices([serviceID]) }
    func centralManager(_ c: CBCentralManager, didFailToConnect p: CBPeripheral, error: Error?) { current=nil; next(); DispatchQueue.main.asyncAfter(deadline:.now()+2) {self.seen.remove(p.identifier)} }
    func centralManager(_ c: CBCentralManager, didDisconnectPeripheral p: CBPeripheral, error: Error?) {
        if connected { fail("Bluetooth peer disconnected") }
        current=nil; rx=nil; tx=nil; next()
        DispatchQueue.main.asyncAfter(deadline:.now()+2) {self.seen.remove(p.identifier)}
    }
    func peripheral(_ p: CBPeripheral, didDiscoverServices error: Error?) {
        if error != nil { central.cancelPeripheralConnection(p); return }
        for s in p.services ?? [] { p.discoverCharacteristics([identityID,rxID,txID],for:s) }
    }
    func peripheral(_ p: CBPeripheral, didDiscoverCharacteristicsFor s: CBService, error: Error?) {
        if error != nil { central.cancelPeripheralConnection(p); return }
        for c in s.characteristics ?? [] {
            if c.uuid == rxID {rx=c}; if c.uuid == txID {tx=c}
        }
        guard rx != nil, tx != nil, let id=s.characteristics?.first(where:{$0.uuid == identityID}) else {central.cancelPeripheralConnection(p); return}
        p.readValue(for:id)
    }
    func peripheral(_ p: CBPeripheral, didUpdateValueFor c: CBCharacteristic, error: Error?) {
        if let e=error {fail("GATT read: \(e)")}
        guard let data=c.value else {fail("empty GATT response")}
        if c.uuid == identityID {
            guard let obj=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any], let id=obj["id"] as? String else {central.cancelPeripheralConnection(p);return}
            if mode == "scan" {emit(["event":"peer","id":id,"name":obj["name"] as? String ?? "", "pairing":obj["pairing"] as? Bool ?? false]);central.cancelPeripheralConnection(p)}
            else if id == target {connected=true;central.stopScan();emit(["event":"ready"])}
            else {central.cancelPeripheralConnection(p)}
        } else if c.uuid == txID {emit(["event":"packet","data":data.base64EncodedString()])}
    }
    func peripheral(_ p: CBPeripheral, didWriteValueFor c: CBCharacteristic, error: Error?) {
        if let e=error {fail("GATT write: \(e)")}
        if let tx=tx {p.readValue(for:tx)}
    }
    func peripheralManagerDidUpdateState(_ m: CBPeripheralManager) {
        check(m.state)
        if m.state == .poweredOn {
            let s=CBMutableService(type:serviceID,primary:true)
            s.characteristics=[
                CBMutableCharacteristic(type:identityID,properties:[.read],value:identity,permissions:[.readable]),
                CBMutableCharacteristic(type:rxID,properties:[.write],value:nil,permissions:[.writeable]),
                CBMutableCharacteristic(type:txID,properties:[.read],value:nil,permissions:[.readable])]
            m.add(s)
        }
    }
    func peripheralManager(_ m: CBPeripheralManager, didAdd service: CBService, error: Error?) {
        if let e=error {fail("GATT service: \(e)")}
        m.startAdvertising([CBAdvertisementDataServiceUUIDsKey:[serviceID]])
    }
    func peripheralManagerDidStartAdvertising(_ m: CBPeripheralManager, error: Error?) {
        if let e=error {fail("BLE advertising: \(e)")};emit(["event":"ready"])
    }
    func peripheralManager(_ m: CBPeripheralManager, didReceiveRead r: CBATTRequest) {
        if r.characteristic.uuid != identityID, let owner=owner, owner != r.central.identifier {m.respond(to:r,withResult:.insufficientResources);return}
        let data=r.characteristic.uuid == identityID ? identity : value
        guard r.offset <= data.count else {m.respond(to:r,withResult:.invalidOffset);return}
        r.value=data.subdata(in:r.offset..<data.count);m.respond(to:r,withResult:.success)
    }
    func peripheralManager(_ m: CBPeripheralManager, didReceiveWrite requests: [CBATTRequest]) {
        for r in requests {
            if let owner=owner, owner != r.central.identifier {m.respond(to:r,withResult:.insufficientResources);return}
            guard r.characteristic.uuid == rxID, r.offset == 0, let data=r.value, data.count >= 8, data.count <= 20 else {m.respond(to:r,withResult:.invalidAttributeValueLength);return}
            owner=r.central.identifier
            emit(["event":"packet","data":data.base64EncodedString()])
        }
        if let r=requests.first {m.respond(to:r,withResult:.success)}
    }
    func command(_ obj:[String:Any]) {
        guard let op=obj["op"] as? String, let str=obj["data"] as? String, let data=Data(base64Encoded:str) else {fail("invalid adapter command")}
        if op == "value" {value=data}
        else if op == "exchange", let p=current, let rx=rx {p.writeValue(data,for:rx,type:.withResponse)}
        else {fail("adapter is not connected")}
    }
}
let adapter=Adapter()
DispatchQueue.global().async {
    while let line=readLine() {
        guard let data=line.data(using:.utf8), let obj=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any] else {fail("invalid JSON command")}
        DispatchQueue.main.async {adapter.command(obj)}
    }
    exit(0)
}
RunLoop.main.run()
