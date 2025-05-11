// macros.rs - Enterprise Procedural Macros Framework
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(proc_macro_diagnostic)]

use proc_macro::TokenStream;
use quote::{quote, quote_spanned};
use syn::{
    parse_macro_input, Attribute, Data, DeriveInput, Error, Expr, Fields,
    GenericParam, Generics, Ident, Lit, Meta, NestedMeta, Type
};

/// Enterprise-grade API endpoint generation
#[proc_macro_attribute]
pub fn api_endpoint(attr: TokenStream, item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as syn::ItemFn);
    
    let func_vis = &input.vis;
    let func_sig = &input.sig;
    let func_block = &input.block;

    let security_check = quote! {
        let __ent_ctx = nuzon_core::SecurityContext::acquire();
        if !__ent_ctx.verify_policy(#attr) {
            return Err(nuzon_core::EnterpriseError::AccessViolation {
                module: module_path!(),
                reason: "Policy violation".into()
            });
        }
    };

    let expanded = quote! {
        #func_vis #func_sig {
            #security_check
            let __ent_span = tracing::info_span!(
                "api_endpoint", 
                endpoint = stringify!(#func_sig),
                policy = #attr
            );
            let __ent_guard = __ent_span.enter();
            
            let __result = (|| #func_block)();
            
            nuzon_core::telemetry::log_latency!(#func_sig, __ent_guard);
            __result
        }
    };

    TokenStream::from(expanded)
}

/// Quantum-safe serialization framework
#[proc_macro_derive(QuantumSerialize, attributes(qs_field))]
pub fn quantum_serialize(input: TokenStream) -> TokenStream {
    let input = parse_macro_input!(input as DeriveInput);
    
    let name = &input.ident;
    let generics = add_trait_bounds(input.generics);
    let (impl_generics, ty_generics, where_clause) = generics.split_for_impl();

    let fields = match input.data {
        Data::Struct(s) => match s.fields {
            Fields::Named(fields) => fields.named,
            _ => unimplemented!(),
        },
        _ => return Error::new_spanned(name, "Only structs supported")
            .to_compile_error()
            .into(),
    };

    let field_encodings = fields.iter().map(|f| {
        let ident = &f.ident;
        let attrs = parse_field_attrs(&f.attrs);
        
        match attrs.encryption {
            Some(algo) => quote! {
                bytes.extend(nuzon_core::crypto::encrypt_hybrid(
                    #algo,
                    &self.#ident.to_string().as_bytes()
                )?);
            },
            None => quote! {
                bytes.extend(serde_json::to_vec(&self.#ident)?);
            },
        }
    });

    let expanded = quote! {
        impl #impl_generics nuzon_core::serialization::QuantumSerialize 
        for #name #ty_generics #where_clause {
            fn quantum_serialize(&self) -> Result<Vec<u8>, nuzon_core::EnterpriseError> {
                use nuzon_core::serialization::QuantumSerialize;
                let mut bytes = Vec::new();
                
                #(#field_encodings)*
                
                Ok(bytes)
            }
        }
    };

    TokenStream::from(expanded)
}

// Validation framework
#[proc_macro]
pub fn validate(input: TokenStream) -> TokenStream {
    let expr = parse_macro_input!(input as Expr);
    
    let validation = quote! {
        match #expr {
            Ok(val) => val,
            Err(e) => {
                tracing::error!(error = %e, "Validation failure");
                return Err(nuzon_core::EnterpriseError::IntegrityError);
            }
        }
    };

    TokenStream::from(validation)
}

// Enterprise tracing instrumentation
#[proc_macro_attribute]
pub fn enterprise_trace(_attr: TokenStream, item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as syn::ItemFn);
    
    let func_vis = &input.vis;
    let func_sig = &input.sig;
    let func_block = &input.block;

    let expanded = quote! {
        #func_vis #func_sig {
            let __ent_span = tracing::trace_span!(
                "enterprise_operation",
                module = module_path!(),
                function = stringify!(#func_sig)
            );
            let __ent_guard = __ent_span.enter();
            
            let __result = #func_block;
            
            nuzon_core::telemetry::log_success!(#func_sig, __ent_guard);
            __result
        }
    };

    TokenStream::from(expanded)
}

fn add_trait_bounds(mut generics: Generics) -> Generics {
    for param in &mut generics.params {
        if let GenericParam::Type(ref mut type_param) = param {
            type_param.bounds.push(syn::parse_str(
                "nuzon_core::serialization::QuantumSerialize"
            ).unwrap());
        }
    }
    generics
}

struct FieldAttributes {
    encryption: Option<String>,
}

fn parse_field_attrs(attrs: &[Attribute]) -> FieldAttributes {
    let mut result = FieldAttributes { encryption: None };
    
    for attr in attrs {
        if attr.path.is_ident("qs_field") {
            attr.parse_nested_meta(|meta| {
                if meta.path.is_ident("encrypt") {
                    let lit: Lit = meta.value()?.parse()?;
                    if let Lit::Str(s) = lit {
                        result.encryption = Some(s.value());
                    }
                    Ok(())
                } else {
                    Err(meta.error("Unsupported qs_field attribute"))
                }
            }).unwrap();
        }
    }
    
    result
}
