using System.Text.Json;
using Windows.Devices.Bluetooth;
using Windows.Devices.Bluetooth.Advertisement;
using Windows.Devices.Bluetooth.GenericAttributeProfile;
using Windows.Storage.Streams;
using Windows.Devices.Enumeration;

static class Adapter {
    static Guid Service = Guid.Parse("8462b001-8c34-4f19-a9ef-8ac72d7312b6");
    static Guid Identity = Guid.Parse("8462b002-8c34-4f19-a9ef-8ac72d7312b6");
    static Guid RX = Guid.Parse("8462b003-8c34-4f19-a9ef-8ac72d7312b6");
    static Guid TX = Guid.Parse("8462b004-8c34-4f19-a9ef-8ac72d7312b6");
    static readonly object OutputLock = new();
    // Keep WinRT event sources alive for the entire listener lifetime. Locals
    // can be collected after Listen awaits, losing their event registrations.
    static GattLocalCharacteristic? localIdentity, localRX, localTX;
    static GattSubscribedClient? subscriber;
    static GattSession? session;
    static readonly SemaphoreSlim Requests = new(1);
    static int pendingRequests;
    static BluetoothLEDevice? device;
    static GattDeviceService? remoteService;
    static GattCharacteristic? rx, tx;
    static GattServiceProvider? provider;
    static BluetoothLEAdvertisementWatcher? watcher;
    static readonly SemaphoreSlim Connecting = new(1);
    static readonly HashSet<ulong> Seen = new();
    static string? owner;
    static bool DiagnosticMode;
    static readonly object OwnerLock = new();
    static void Emit(object o) { lock(OutputLock) {Console.WriteLine(JsonSerializer.Serialize(o));Console.Out.Flush();} }
    static void Fail(string text) {Emit(new { @event="error",error=text });Environment.Exit(1);}
    static void Debug(string text) {if(!string.IsNullOrEmpty(Environment.GetEnvironmentVariable("BTFLING_BLE_DEBUG")))Console.Error.WriteLine("Windows BLE: "+text);}
    static byte[] Bytes(IBuffer b) {using var r=DataReader.FromBuffer(b);var data=new byte[b.Length];r.ReadBytes(data);return data;}
    static IBuffer Buffer(byte[] b) {using var w=new DataWriter();w.WriteBytes(b);return w.DetachBuffer();}
    static void Check(GattCommunicationStatus s) {if(s!=GattCommunicationStatus.Success)throw new IOException("GATT: "+s);}
    static async Task<string> DescribeAdapter(BluetoothAdapter adapter) {
        var name="unknown";
        try {
            using var timeout=new CancellationTokenSource(TimeSpan.FromSeconds(2));
            var info=await System.WindowsRuntimeSystemExtensions.AsTask(DeviceInformation.CreateFromIdAsync(adapter.DeviceId),timeout.Token);
            if(info!=null)name=info.Name;
        }catch { /* Diagnostics must not prevent radio startup. */ }
        return $"OS={Environment.OSVersion.VersionString}; adapter={name}; device={adapter.DeviceId}; BLE={adapter.IsLowEnergySupported}; central={adapter.IsCentralRoleSupported}; peripheral={adapter.IsPeripheralRoleSupported}";
    }
    static async Task Listen(string id,string name,bool pairing) {
        var adapter=await BluetoothAdapter.GetDefaultAsync();
        if(adapter==null)throw new IOException("No Bluetooth adapter found");
        var adapterDetails=await DescribeAdapter(adapter);
        Debug(adapterDetails);
        if(DiagnosticMode)Emit(new{@event="diagnostic",phase="adapter",error=adapterDetails});
        if(!adapter.IsPeripheralRoleSupported)throw new IOException("This Bluetooth adapter/driver does not support BLE peripheral mode; "+adapterDetails);
        var result=await GattServiceProvider.CreateAsync(Service);
        if(result.Error!=BluetoothError.Success)throw new IOException("GATT service: "+result.Error);
        provider=result.ServiceProvider;
        var info=await provider.Service.CreateCharacteristicAsync(Identity,new GattLocalCharacteristicParameters {
            CharacteristicProperties=GattCharacteristicProperties.Read,
            ReadProtectionLevel=GattProtectionLevel.Plain });
        if(info.Error!=BluetoothError.Success)throw new IOException("Identity characteristic: "+info.Error);
        localIdentity=info.Characteristic;
        localIdentity.ReadRequested+=async (_,args)=>{
            var def=args.GetDeferral();
            try{var req=await args.GetRequestAsync();if(req==null)return;
                bool accepting;lock(OwnerLock){accepting=owner==null;}
                var value=JsonSerializer.SerializeToUtf8Bytes(new{version=1,id,name,pairing,accepting,diagnostic=DiagnosticMode});
                Debug($"Identity read: offset={req.Offset}; length={req.Length}; total={value.Length}");
                if(!IdentityRead.TrySlice(value,req.Offset,req.Length,out var response)){
                    req.RespondWithProtocolError(GattProtocolError.InvalidOffset);return;
                }
                req.RespondWithValue(Buffer(response));
            }catch(Exception e){Fail("Identity read: "+e.Message);}finally{def.Complete();}
        };
        var r=await provider.Service.CreateCharacteristicAsync(RX,new GattLocalCharacteristicParameters {
            CharacteristicProperties=GattCharacteristicProperties.WriteWithoutResponse, WriteProtectionLevel=GattProtectionLevel.Plain });
        var t=await provider.Service.CreateCharacteristicAsync(TX,new GattLocalCharacteristicParameters {
            CharacteristicProperties=GattCharacteristicProperties.Notify, ReadProtectionLevel=GattProtectionLevel.Plain });
        if(r.Error!=BluetoothError.Success || t.Error!=BluetoothError.Success)throw new IOException("Cannot create streaming characteristics");
        localRX=r.Characteristic;localTX=t.Characteristic;
        var subscribed=new TaskCompletionSource();
        var sizeReady=new TaskCompletionSource();
        var advertising=new AdvertisingLifecycle();
        localTX.SubscribedClientsChanged+=(_,_)=>{
            lock(OwnerLock){
                if(subscriber!=null){
                    if(!localTX.SubscribedClients.Any(c=>c.Session.DeviceId.Id==owner))Fail("Bluetooth notification subscriber disconnected");
                    return;
                }
                var client=localTX.SubscribedClients.FirstOrDefault();if(client==null)return;
                subscriber=client;owner=client.Session.DeviceId.Id;
                Debug($"Incoming subscription; max_notification={client.MaxNotificationSize}");
                client.MaxNotificationSizeChanged+=(_,_)=>{if(client.MaxNotificationSize>36)sizeReady.TrySetResult();Emit(new{@event="size",max_value=Math.Min(512,(int)client.MaxNotificationSize)});};
                if(client.MaxNotificationSize>36)sizeReady.TrySetResult();
                client.Session.SessionStatusChanged+=(_,a)=>{if(a.Status==GattSessionStatus.Closed)Fail("Bluetooth session closed");};
                advertising.StopRequested();provider.StopAdvertising();subscribed.TrySetResult();
            }
        };
        localRX.WriteRequested+=async (_,args)=>{
            var def=args.GetDeferral();
            if(Interlocked.Increment(ref pendingRequests)>64)Fail("GATT receive callback queue overflow");
            await Requests.WaitAsync();
            try {
                var req=await args.GetRequestAsync();if(req==null)throw new IOException("Missing GATT write");
                lock(OwnerLock){if(owner!=args.Session.DeviceId.Id)return;}
                var b=Bytes(req.Value);
                if(req.Offset!=0 || b.Length<36 || b.Length>512 || req.Option!=GattWriteOption.WriteWithoutResponse)throw new IOException("Malformed GATT stream write");
                Emit(new{@event="packet",data=Convert.ToBase64String(b)});
            } catch(Exception e){Fail("GATT write request: "+e.Message);} finally{Interlocked.Decrement(ref pendingRequests);Requests.Release();def.Complete();}
        };
        provider.AdvertisementStatusChanged+=(_,args)=>{
            advertising.Observe(args.Status.ToString(),args.Error==BluetoothError.Success?null:$"{args.Error} ({(int)args.Error})");
            Debug($"Advertising status={args.Status}; error={args.Error} ({(int)args.Error}); {adapterDetails}");
        };
        try {
            provider.StartAdvertising(new GattServiceProviderAdvertisingParameters {IsConnectable=true,IsDiscoverable=true});
            await advertising.Ready.WaitAsync(TimeSpan.FromSeconds(20));
            await Task.WhenAny(subscribed.Task,advertising.Failure);
            if(advertising.Failure.IsCompleted)throw await advertising.Failure;
            await subscribed.Task;
        }catch(TimeoutException e){throw new IOException($"Windows did not confirm BLE advertising within 20 seconds; advertising={advertising.History}; {adapterDetails}",e);}
        catch(Exception e){throw new IOException($"{e.Message}; {adapterDetails}",e);}
        try{await sizeReady.Task.WaitAsync(TimeSpan.FromSeconds(10));}catch(TimeoutException){throw new IOException("BLE negotiated value is too small: streaming requires at least 37 bytes");}
        Emit(new{@event="ready",max_value=Math.Min(512,(int)subscriber!.MaxNotificationSize)});
    }
    static void Scan(string mode,string target) {
        watcher=new BluetoothLEAdvertisementWatcher {ScanningMode=BluetoothLEScanningMode.Active};
        watcher.AdvertisementFilter.Advertisement.ServiceUuids.Add(Service);
        watcher.Stopped+=(_,args)=>{if(args.Error!=BluetoothError.Success)Fail("Bluetooth scan stopped: "+args.Error);};
        watcher.Received+=(sender,args)=>{_ = Inspect(args.BluetoothAddress,mode,target);};watcher.Start();
    }
    static bool MarkSeen(ulong address){lock(Seen){return Seen.Count<256 && Seen.Add(address);}}
    static async Task Inspect(ulong address,string mode,string target) {
        if(!MarkSeen(address))return;
        await Connecting.WaitAsync();
        try {
            if(device!=null)return;
            using var timeout=new CancellationTokenSource(TimeSpan.FromSeconds(10));
            // AsTask cancellation bounds discovery even for stale advertisements.
            var d=await System.WindowsRuntimeSystemExtensions.AsTask(BluetoothLEDevice.FromBluetoothAddressAsync(address),timeout.Token);
            if(d==null)return;
            bool keep=false;bool selected=false;
            try {
                var ss=await System.WindowsRuntimeSystemExtensions.AsTask(d.GetGattServicesForUuidAsync(Service,BluetoothCacheMode.Uncached),timeout.Token);Check(ss.Status);
                foreach(var s in ss.Services) {
                    var cs=await System.WindowsRuntimeSystemExtensions.AsTask(s.GetCharacteristicsAsync(BluetoothCacheMode.Uncached),timeout.Token);Check(cs.Status);
                    var identity=cs.Characteristics.FirstOrDefault(c=>c.Uuid==Identity);
                    if(identity==null){s.Dispose();continue;}
                    var read=await System.WindowsRuntimeSystemExtensions.AsTask(identity.ReadValueAsync(BluetoothCacheMode.Uncached),timeout.Token);Check(read.Status);
                    using var doc=JsonDocument.Parse(Bytes(read.Value));var root=doc.RootElement;
                    var id=root.GetProperty("id").GetString();var name=root.GetProperty("name").GetString();
                    if(root.GetProperty("version").GetInt32()!=1){if(id==target)Fail("Unsupported btfling protocol version");continue;}
                    var diagnostic=root.TryGetProperty("diagnostic",out var diagnosticFlag) && diagnosticFlag.GetBoolean();
                    Debug($"Identity read: id={id}; name={name}; diagnostic={diagnostic}");
                    if(mode=="scan")Emit(new{@event="peer",version=1,id,name,pairing=root.TryGetProperty("pairing",out var flag) && flag.GetBoolean(),diagnostic});
                    else if(id==target) {
                        if(diagnostic!=DiagnosticMode)throw new IOException("Peer is running a different pairing mode");
                        selected=true;rx=cs.Characteristics.First(c=>c.Uuid==RX);tx=cs.Characteristics.First(c=>c.Uuid==TX);
                        if(!rx.CharacteristicProperties.HasFlag(GattCharacteristicProperties.WriteWithoutResponse) || !tx.CharacteristicProperties.HasFlag(GattCharacteristicProperties.Notify))throw new IOException("Incompatible streaming characteristics");
                        session=await System.WindowsRuntimeSystemExtensions.AsTask(GattSession.FromDeviceIdAsync(d.BluetoothDeviceId),timeout.Token);
                        if(session==null)throw new IOException("Missing GATT session");
                        session.MaintainConnection=true;
                        var sizeReady=new TaskCompletionSource();
                        session.MaxPduSizeChanged+=(_,_)=>{if(session.MaxPduSize-3>36)sizeReady.TrySetResult();Emit(new{@event="size",max_value=Math.Min(512,session.MaxPduSize-3)});};
                        tx.ValueChanged+=(_,a)=>Emit(new{@event="packet",data=Convert.ToBase64String(Bytes(a.CharacteristicValue))});
                        Check(await System.WindowsRuntimeSystemExtensions.AsTask(tx.WriteClientCharacteristicConfigurationDescriptorAsync(GattClientCharacteristicConfigurationDescriptorValue.Notify),timeout.Token));
                        if(session.MaxPduSize-3>36)sizeReady.TrySetResult();
                        try{await sizeReady.Task.WaitAsync(TimeSpan.FromSeconds(10));}catch(TimeoutException){throw new IOException("BLE negotiated value is too small: streaming requires at least 37 bytes");}
                        device=d;remoteService=s;keep=true;watcher!.Stop();
                        d.ConnectionStatusChanged+=(_,_)=>{if(d.ConnectionStatus==BluetoothConnectionStatus.Disconnected)Fail("Bluetooth peer disconnected");};
                        Emit(new{@event="ready",max_value=Math.Min(512,session.MaxPduSize-3)});return;
                    }
                    s.Dispose();
                }
            }catch(Exception e){if(selected)Fail("Connect: "+e.Message);throw;}finally{if(!keep)d.Dispose();}
        } catch(Exception e) { Debug($"Inspect {address:X12} failed: {e.Message}"); /* Keep discovering unrelated/vanished peers. */ }
        finally{Connecting.Release();await Task.Delay(2000);lock(Seen){Seen.Remove(address);}}
    }
    public static async Task Main(string[] args) {
        if(args.Length==1 && args[0]=="--version"){Console.WriteLine("btfling-ble wire-v1");return;}
        try {
            if(args[0].StartsWith("diagnose-")){
                DiagnosticMode=true;args[0]=args[0]["diagnose-".Length..];
                Service=Guid.Parse("8462b101-8c34-4f19-a9ef-8ac72d7312b6");
                Identity=Guid.Parse("8462b102-8c34-4f19-a9ef-8ac72d7312b6");
                RX=Guid.Parse("8462b103-8c34-4f19-a9ef-8ac72d7312b6");
                TX=Guid.Parse("8462b104-8c34-4f19-a9ef-8ac72d7312b6");
            }
            Debug($"mode={args[0]}; diagnostic={DiagnosticMode}; service={Service}");
            if(args[0]=="listen" || args[0]=="pair-listen")await Listen(args[1],args[2],args[0]=="pair-listen");else Scan(args[0],args[1]);
            string? line;
            while((line=await Console.In.ReadLineAsync())!=null) {
                using var doc=JsonDocument.Parse(line);var root=doc.RootElement;
                var data=Convert.FromBase64String(root.GetProperty("data").GetString()!);
                if(root.GetProperty("op").GetString()!="send" || data.Length<36 || data.Length>512)throw new IOException("Invalid adapter command");
                if(localTX!=null && subscriber!=null){
                    if(data.Length>subscriber.MaxNotificationSize)throw new IOException("Notification size shrank");
                    var result=await localTX.NotifyValueAsync(Buffer(data),subscriber);Check(result.Status);
                } else {
                    if(rx==null || session==null)throw new IOException("Not connected");
                    if(data.Length>session.MaxPduSize-3)throw new IOException("Write size shrank");
                    Check(await rx.WriteValueAsync(Buffer(data),GattWriteOption.WriteWithoutResponse));
                }
                Emit(new{@event="sent"});
            }
        }catch(Exception e){Fail(e.Message);}
        finally{watcher?.Stop();provider?.StopAdvertising();session?.Dispose();remoteService?.Dispose();device?.Dispose();}
    }
}
