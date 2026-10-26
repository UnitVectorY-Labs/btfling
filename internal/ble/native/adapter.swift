import Foundation
import CoreBluetooth

let outputLock=NSLock()
func emit(_ value: [String:Any]) {
    outputLock.lock();defer{outputLock.unlock()}
    if let data = try? JSONSerialization.data(withJSONObject: value) {
        FileHandle.standardOutput.write(data); FileHandle.standardOutput.write(Data([10]))
    }
}
func fail(_ text: String) -> Never { emit(["event":"error", "error":text]); exit(1) }
if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--version" {
    print("btfling-ble wire-v1")
    exit(0)
}
let diagnostic = CommandLine.arguments[1].hasPrefix("diagnose-")
let mode = diagnostic ? String(CommandLine.arguments[1].dropFirst("diagnose-".count)) : CommandLine.arguments[1]
let serviceID = CBUUID(string: (diagnostic ? "8462b101" : "8462b001")+"-8c34-4f19-a9ef-8ac72d7312b6")
let identityID = CBUUID(string: (diagnostic ? "8462b102" : "8462b002")+"-8c34-4f19-a9ef-8ac72d7312b6")
let rxID = CBUUID(string: (diagnostic ? "8462b103" : "8462b003")+"-8c34-4f19-a9ef-8ac72d7312b6")
let txID = CBUUID(string: (diagnostic ? "8462b104" : "8462b004")+"-8c34-4f19-a9ef-8ac72d7312b6")
func debug(_ text:String) {
    if ProcessInfo.processInfo.environment["BTFLING_BLE_DEBUG"] != nil {
        FileHandle.standardError.write(Data("macOS BLE: \(text)\n".utf8))
    }
}
let target = CommandLine.arguments.count > 2 ? CommandLine.arguments[2] : ""
let nickname = CommandLine.arguments.count > 3 ? CommandLine.arguments[3] : ""
debug("OS=\(ProcessInfo.processInfo.operatingSystemVersionString); mode=\(mode); diagnostic=\(diagnostic); service=\(serviceID)")
if diagnostic {emit(["event":"diagnostic","phase":"adapter","error":"OS=\(ProcessInfo.processInfo.operatingSystemVersionString); mode=\(mode); service=\(serviceID)"])}

final class Adapter: NSObject, CBCentralManagerDelegate, CBPeripheralDelegate, CBPeripheralManagerDelegate {
    var central: CBCentralManager!
    var manager: CBPeripheralManager!
    var current: CBPeripheral?
    var pending: [CBPeripheral] = []
    var seen = Set<UUID>()
    var rx: CBCharacteristic?
    var tx: CBCharacteristic?
    var localTX: CBMutableCharacteristic?
    var subscriber: CBCentral?
    var outgoing: [Data] = []
    var identity = Data()
    var owner: UUID?
    var connected = false
    var lastMax = 0
    override init() {
        super.init()
        if mode == "listen" || mode == "pair-listen" {
            identity = try! JSONSerialization.data(withJSONObject: ["version":1,"id":target,"name":nickname,"pairing":mode == "pair-listen", "accepting":true,"diagnostic":diagnostic])
            manager = CBPeripheralManager(delegate:self, queue: .main)
        } else { central = CBCentralManager(delegate:self, queue: .main) }
    }
    func check(_ state: CBManagerState) {
        debug("manager state=\(state.rawValue), mode=\(mode), diagnostic=\(diagnostic)")
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
        debug("advertisement from \(p.identifier), RSSI=\(RSSI)")
        if seen.count < 256 && seen.insert(p.identifier).inserted { pending.append(p); next() }
    }
    func next() {
        if current != nil || pending.isEmpty { return }
        let p = pending.removeFirst(); current = p; p.delegate = self;debug("connecting to \(p.identifier)"); central.connect(p)
        DispatchQueue.main.asyncAfter(deadline:.now()+10) { [weak self, weak p] in
            guard let self=self, let p=p else{return}
            if self.current === p && !self.connected { self.central.cancelPeripheralConnection(p) }
        }
    }
    func centralManager(_ c: CBCentralManager, didConnect p: CBPeripheral) {debug("connected to \(p.identifier); discovering services"); p.discoverServices([serviceID]) }
    func centralManager(_ c: CBCentralManager, didFailToConnect p: CBPeripheral, error: Error?) {
        debug("connect failed for \(p.identifier): \(String(describing:error))")
        guard current === p else {return}
        current=nil; next(); DispatchQueue.main.asyncAfter(deadline:.now()+2) {self.seen.remove(p.identifier)}
    }
    func centralManager(_ c: CBCentralManager, didDisconnectPeripheral p: CBPeripheral, error: Error?) {
        debug("disconnected from \(p.identifier): \(String(describing:error))")
        guard current === p else {return}
        if connected { fail("Bluetooth peer disconnected") }
        current=nil; rx=nil; tx=nil; next()
        DispatchQueue.main.asyncAfter(deadline:.now()+2) {self.seen.remove(p.identifier)}
    }
    func peripheral(_ p: CBPeripheral, didDiscoverServices error: Error?) {
        if let e=error {debug("service discovery failed: \(e)");central.cancelPeripheralConnection(p);return}
        for s in p.services ?? [] { p.discoverCharacteristics([identityID,rxID,txID],for:s) }
    }
    func peripheral(_ p: CBPeripheral, didDiscoverCharacteristicsFor s: CBService, error: Error?) {
        if let e=error {debug("characteristic discovery failed: \(e)");central.cancelPeripheralConnection(p);return}
        for c in s.characteristics ?? [] {
            if c.uuid == rxID {rx=c}; if c.uuid == txID {tx=c}
        }
        guard rx != nil, tx != nil, let id=s.characteristics?.first(where:{$0.uuid == identityID}) else {central.cancelPeripheralConnection(p); return}
        p.readValue(for:id)
    }
    func peripheral(_ p: CBPeripheral, didUpdateValueFor c: CBCharacteristic, error: Error?) {
        // An advertisement can vanish or reject a read while we inspect it.
        // Discovery must keep running, just like the Windows scanner does.
        if c.uuid == identityID, error != nil || c.value == nil {
            if ProcessInfo.processInfo.environment["BTFLING_BLE_DEBUG"] != nil {
                let detail=error.map {String(describing:$0)} ?? "empty response"
                FileHandle.standardError.write(Data("Identity read from \(p.identifier) failed; retrying: \(detail)\n".utf8))
            }
            central.cancelPeripheralConnection(p);return
        }
        if let e=error {fail("GATT value \(c.uuid): \(e)")}
        guard let data=c.value else {fail("empty GATT response for \(c.uuid)")}
        if c.uuid == identityID {
            guard let obj=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any], let id=obj["id"] as? String else {central.cancelPeripheralConnection(p);return}
            guard let version=obj["version"] as? Int, version == 1 else {if id == target {fail("Unsupported btfling protocol version")};central.cancelPeripheralConnection(p);return}
            debug("identity id=\(id), diagnostic=\(obj["diagnostic"] as? Bool ?? false)")
            if mode == "scan" {emit(["event":"peer","version":1,"id":id,"name":obj["name"] as? String ?? "", "pairing":obj["pairing"] as? Bool ?? false,"diagnostic":obj["diagnostic"] as? Bool ?? false]);central.cancelPeripheralConnection(p)}
            else if id == target {
                guard (obj["diagnostic"] as? Bool ?? false) == diagnostic else {fail("Peer is running a different pairing mode")}
                guard let rx=rx, let tx=tx, rx.properties.contains(.writeWithoutResponse), tx.properties.contains(.notify) else {fail("Incompatible streaming characteristics")}
                central.stopScan();p.setNotifyValue(true,for:tx)
            }
            else {central.cancelPeripheralConnection(p)}
        } else if c.uuid == txID {emit(["event":"packet","data":data.base64EncodedString()])}
    }
    func peripheral(_ p: CBPeripheral, didUpdateNotificationStateFor c: CBCharacteristic, error: Error?) {
        if let e=error {fail("Subscribe: \(e)")}
        guard c.isNotifying else {fail("Notification subscription ended")}
        connected=true;lastMax=min(512,p.maximumWriteValueLength(for:.withoutResponse))
        debug("notification subscription ready; maximum write=\(lastMax)")
        emit(["event":"ready","max_value":lastMax])
    }
    func peripheralIsReady(toSendWriteWithoutResponse p: CBPeripheral) { drain() }
    func peripheralManagerIsReady(toUpdateSubscribers m: CBPeripheralManager) { drain() }
    func peripheralManager(_ m: CBPeripheralManager, central c: CBCentral, didSubscribeTo characteristic: CBCharacteristic) {
        guard characteristic.uuid == txID else {return}
        if owner != nil {return}
        owner=c.identifier;subscriber=c;connected=true;m.stopAdvertising()
        identity=try! JSONSerialization.data(withJSONObject:["version":1,"id":target,"name":nickname,"pairing":mode == "pair-listen","accepting":false,"diagnostic":diagnostic])
        debug("incoming subscriber \(c.identifier); maximum notification=\(c.maximumUpdateValueLength)")
        lastMax=min(512,c.maximumUpdateValueLength)
        emit(["event":"ready","max_value":lastMax])
    }
    func peripheralManager(_ m: CBPeripheralManager, central c: CBCentral, didUnsubscribeFrom characteristic: CBCharacteristic) {
        if owner == c.identifier {fail("Bluetooth notification subscriber disconnected")}
    }
    func drain() {
        let maximum = subscriber.map {min(512,$0.maximumUpdateValueLength)} ?? current.map {min(512,$0.maximumWriteValueLength(for:.withoutResponse))} ?? 0
        if maximum != lastMax && maximum > 0 {lastMax=maximum;emit(["event":"size","max_value":maximum])}
        while let data=outgoing.first {
            if let m=manager, let c=subscriber, let tx=localTX {
                guard data.count <= min(512,c.maximumUpdateValueLength) else {fail("Notification size shrank")}
                if !m.updateValue(data,for:tx,onSubscribedCentrals:[c]) {return}
            } else if let p=current, let rx=rx {
                guard data.count <= min(512,p.maximumWriteValueLength(for:.withoutResponse)) else {fail("Write size shrank")}
                if !p.canSendWriteWithoutResponse {return}
                p.writeValue(data,for:rx,type:.withoutResponse)
            } else {fail("Adapter is not connected")}
            outgoing.removeFirst();emit(["event":"sent"])
        }
    }
    func peripheralManagerDidUpdateState(_ m: CBPeripheralManager) {
        check(m.state)
        if m.state == .poweredOn {
            let s=CBMutableService(type:serviceID,primary:true)
            let notify=CBMutableCharacteristic(type:txID,properties:[.notify],value:nil,permissions:[.readable])
            localTX=notify
            s.characteristics=[
                CBMutableCharacteristic(type:identityID,properties:[.read],value:nil,permissions:[.readable]),
                CBMutableCharacteristic(type:rxID,properties:[.writeWithoutResponse],value:nil,permissions:[.writeable]),
                notify]
            m.add(s)
        }
    }
    func peripheralManager(_ m: CBPeripheralManager, didAdd service: CBService, error: Error?) {
        if let e=error {fail("GATT service: \(e)")}
        m.startAdvertising([CBAdvertisementDataServiceUUIDsKey:[serviceID]])
    }
    func peripheralManagerDidStartAdvertising(_ m: CBPeripheralManager, error: Error?) {
        if let e=error {fail("BLE advertising: \(e)")}
        debug("advertising started; diagnostic=\(diagnostic)")
    }
    func peripheralManager(_ m: CBPeripheralManager, didReceiveRead r: CBATTRequest) {
        debug("identity read: offset=\(r.offset); total=\(identity.count)")
        guard r.characteristic.uuid == identityID else {m.respond(to:r,withResult:.readNotPermitted);return}
        let data=identity
        guard r.offset <= data.count else {m.respond(to:r,withResult:.invalidOffset);return}
        r.value=data.subdata(in:r.offset..<data.count);m.respond(to:r,withResult:.success)
    }
    func peripheralManager(_ m: CBPeripheralManager, didReceiveWrite requests: [CBATTRequest]) {
        for r in requests {
            // Write Commands have no ATT response. Ownership comes from subscription.
            guard owner == r.central.identifier else {continue}
            guard r.characteristic.uuid == rxID, r.offset == 0, let data=r.value, data.count >= 36, data.count <= 512 else {fail("Malformed GATT stream write")}
            emit(["event":"packet","data":data.base64EncodedString()])
        }
    }
    func command(_ obj:[String:Any]) {
        guard obj["op"] as? String == "send", let str=obj["data"] as? String, let data=Data(base64Encoded:str), data.count >= 36, data.count <= 512 else {fail("Invalid adapter command")}
        guard outgoing.count < 16 else {fail("Native admission queue overflow")}
        outgoing.append(data);drain()
    }

}
let adapter=Adapter()
let commandSlots=DispatchSemaphore(value:16)
DispatchQueue.global().async {
    while let line=readLine() {
        guard let data=line.data(using:.utf8), let obj=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any] else {fail("invalid JSON command")}
        commandSlots.wait()
        DispatchQueue.main.async {adapter.command(obj);commandSlots.signal()}
    }
    exit(0)
}
RunLoop.main.run()
