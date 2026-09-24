package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.uber.org/zap"
)

// scopeName 是上报时 instrumentation scope 的名字。
const scopeName = "aladdin"

// 服务标识。由各入口在构建时声明，不由配置提供：
// 同一个二进制在不同环境报告成不同服务名，会让"这是哪个服务"变成需要猜的问题。
const (
	ServiceServer = "aladdin-server"
	ServiceCLI    = "aladdin-cli"
)

// ProviderOptions 是追踪与指标的构建参数。
type ProviderOptions struct {
	// ServiceName 是上报时的 service.name。空值会退化成 scopeName，
	// 因此入口必须显式传 ServiceServer 或 ServiceCLI。
	ServiceName string
	// ServiceVersion 是上报时的 service.version。
	ServiceVersion string
	// Endpoint 是 OTLP/HTTP 端点（host:port）。为空表示不上报。
	Endpoint string
	// Insecure 为真时用明文 HTTP 连接端点。本地 Collector 通常需要。
	Insecure bool
	// SampleRatio 是采样比例，取值 (0, 1]。越界由配置校验拦下。
	SampleRatio float64
	// Logger 接收 SDK 自身的错误（如导出失败）。
	//
	// 导出失败是"端点暂时不可达"这类预期内的事情，因此只记 DEBUG：
	// 记到 WARN 会让真正的故障淹没在重试日志里。为 nil 时丢弃。
	Logger *zap.Logger
}

// Provider 持有本进程的追踪与指标实现。
//
// 它必须随进程生命周期存在，并在退出前 Shutdown 以冲刷最后一批数据。
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
}

// NewProvider 构建追踪与指标实现。
//
// 两个刻意的设计：
//
//   - **传播器无论如何都设置**。链路标识的生成与传播与是否上报无关，
//     因此即使没有配置端点，trace_id / span_id 也照常生成、传播、回写响应头。
//     若把"生成"和"上报"绑在一起，没有 Collector 的环境会连关联能力一起失去。
//   - **没有端点就不挂导出器**，SDK 也就不会尝试连接、不会刷连接错误。
func NewProvider(ctx context.Context, opts ProviderOptions) (*Provider, error) {
	name := opts.ServiceName
	if name == "" {
		name = scopeName
	}

	// 用 schemaless 构造自己的属性，再与默认 resource 合并。
	//
	// 不用 semconv 包里的带版本构造函数：它的 schema URL 必须与
	// resource.Default() 的实现版本逐字相同，否则 Merge 会以
	// "conflicting Schema URL" 失败——而那是一个只在升级 OTel 时才暴露、
	// 且会让进程起不来的耦合。服务名与版本号不受语义约定版本影响，
	// 因此显式不带 schema URL 才是稳的。
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(name),
		semconv.ServiceVersion(opts.ServiceVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("构建 OTel resource 失败: %w", err)
	}

	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		if opts.Logger != nil {
			opts.Logger.Debug("OTel 上报出错", zap.Error(err))
		}
	}))

	traceOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFor(opts.SampleRatio)),
	}
	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	if opts.Endpoint != "" {
		traceExporter, err := newTraceExporter(ctx, opts)
		if err != nil {
			return nil, err
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(traceExporter))

		metricExporter, err := newMetricExporter(ctx, opts)
		if err != nil {
			return nil, err
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))
	}

	provider := &Provider{
		tracerProvider: sdktrace.NewTracerProvider(traceOpts...),
		meterProvider:  sdkmetric.NewMeterProvider(metricOpts...),
	}
	otel.SetTracerProvider(provider.tracerProvider)
	otel.SetMeterProvider(provider.meterProvider)
	return provider, nil
}

// Shutdown 冲刷并关闭两个 provider。
//
// 调用方必须给它一个尚未取消的 context：进程收到停止信号时用来取消监听的那个
// context 已经失效，直接拿它调用会导致一次都冲刷不了。
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var errs []error
	if err := p.tracerProvider.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("关闭 tracer provider: %w", err))
	}
	if err := p.meterProvider.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("关闭 meter provider: %w", err))
	}
	// 两个错误都要报出来：只报第一个会让人以为另一个是好的。
	return joinErrors(errs)
}

// samplerFor 把比例转成采样器。
//
// 有上游时跟随上游的决定，没有上游时按比例采样。采样只影响 span 是否被记录与
// 导出，不影响 ID 的生成——被判为丢弃的 span 依然持有合法的 trace_id/span_id，
// 日志关联照常工作。
func samplerFor(ratio float64) sdktrace.Sampler {
	if ratio >= 1 || ratio <= 0 {
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

func newTraceExporter(ctx context.Context, opts ProviderOptions) (sdktrace.SpanExporter, error) {
	exporterOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(opts.Endpoint)}
	if opts.Insecure {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("构建 OTLP trace 导出器失败: %w", err)
	}
	return exporter, nil
}

func newMetricExporter(ctx context.Context, opts ProviderOptions) (sdkmetric.Exporter, error) {
	exporterOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(opts.Endpoint)}
	if opts.Insecure {
		exporterOpts = append(exporterOpts, otlpmetrichttp.WithInsecure())
	}
	exporter, err := otlpmetrichttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("构建 OTLP metric 导出器失败: %w", err)
	}
	return exporter, nil
}

func joinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%w; %w", errs[0], errs[1])
	}
}
