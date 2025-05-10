// edi_parser.rs - Enterprise EDIFACT Processing Engine
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(iterator_try_collect)]

use std::{collections::HashMap, str::Chars, iter::Peekable};
use thiserror::Error;
use serde::{Serialize, Deserialize};
use regex::Regex;
use tracing::{info_span, instrument};

/// EDIFACT parse error hierarchy
#[derive(Error, Debug, Clone, Eq, PartialEq)]
pub enum EdiError {
    #[error("Syntax error at position {position}: {details}")]
    SyntaxError {
        position: usize,
        details: String,
    },
    #[error("Invalid service string advice")]
    InvalidServiceStringAdvice,
    #[error("Unsupported EDIFACT version: {0}")]
    UnsupportedVersion(String),
    #[error("Segment missing mandatory elements")]
    MandatoryElementMissing,
    #[error("Validation rule violation: {0}")]
    ValidationError(String),
}

/// Represents EDIFACT interchange control parameters
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EdifactInterchange {
    pub unb: UnbSegment,
    pub messages: Vec<EdifactMessage>,
    pub unz: UnzSegment,
}

/// UNB segment structure (Interchange Header)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UnbSegment {
    pub syntax_identifier: String,
    pub syntax_version: String,
    pub sender_identification: String,
    pub recipient_identification: String,
    pub preparation_time: String,
    pub control_reference: String,
    pub application_reference: String,
}

/// UNZ segment structure (Interchange Trailer)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UnzSegment {
    pub interchange_control_count: u32,
    pub interchange_control_reference: String,
}

/// Complete EDIFACT message structure
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EdifactMessage {
    pub unh: UnhSegment,
    pub segments: Vec<EdifactSegment>,
    pub unt: UntSegment,
}

/// UNH segment structure (Message Header)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UnhSegment {
    pub message_reference_number: String,
    pub message_identifier: String,
    pub message_version: String,
    pub message_release: String,
    pub controlling_agency: String,
}

/// UNT segment structure (Message Trailer)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UntSegment {
    pub segment_count: u32,
    pub message_reference_number: String,
}

/// EDIFACT segment with dynamic element handling
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EdifactSegment {
    pub tag: String,
    pub elements: Vec<EdifactElement>,
}

/// EDIFACT element with component support
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EdifactElement {
    pub components: Vec<String>,
}

/// Main parser implementation
pub struct EdiParser<'a> {
    chars: Peekable<Chars<'a>>,
    position: usize,
    delimiters: EdiDelimiters,
    config: ParserConfig,
}

/// EDIFACT delimiter set from service string advice
#[derive(Debug, Clone)]
struct EdiDelimiters {
    component_separator: char,
    data_separator: char,
    decimal_separator: char,
    escape_character: char,
    segment_terminator: char,
}

/// Parser configuration parameters
#[derive(Debug, Clone)]
pub struct ParserConfig {
    pub validate_structure: bool,
    pub strict_mode: bool,
    pub max_segment_length: usize,
    pub allowed_versions: Vec<String>,
}

impl Default for ParserConfig {
    fn default() -> Self {
        Self {
            validate_structure: true,
            strict_mode: false,
            max_segment_length: 4096,
            allowed_versions: vec!["D".into(), "01B".into(), "02B".into()],
        }
    }
}

impl<'a> EdiParser<'a> {
    /// Create new parser instance with custom configuration
    pub fn new(input: &'a str, config: ParserConfig) -> Result<Self, EdiError> {
        let mut parser = Self {
            chars: input.chars().peekable(),
            position: 0,
            delimiters: EdiDelimiters::default(),
            config,
        };
        
        parser.parse_service_string_advice()?;
        Ok(parser)
    }

    /// Main parsing entry point
    #[instrument(name = "EDIFACT parsing", skip(self))]
    pub fn parse_interchange(&mut self) -> Result<EdifactInterchange, EdiError> {
        let _span = info_span!("parse_interchange").entered();
        
        let unb = self.parse_unb()?;
        self.validate_version(&unb.syntax_version)?;

        let mut messages = Vec::new();
        while self.peek_segment_tag()? == "UNH" {
            messages.push(self.parse_message()?);
        }

        let unz = self.parse_unz()?;

        if self.config.validate_structure {
            self.validate_interchange(&unb, &unz, messages.len())?;
        }

        Ok(EdifactInterchange { unb, messages, unz })
    }

    /// Parse UNB segment with service string advice
    fn parse_unb(&mut self) -> Result<UnbSegment, EdiError> {
        let tag = self.parse_segment_tag()?;
        if tag != "UNB" {
            return Err(self.error(EdiError::SyntaxError {
                position: self.position,
                details: format!("Expected UNB segment, found {}", tag),
            }));
        }

        Ok(UnbSegment {
            syntax_identifier: self.parse_element()?,
            syntax_version: self.parse_element()?,
            sender_identification: self.parse_element()?,
            recipient_identification: self.parse_element()?,
            preparation_time: self.parse_element()?,
            control_reference: self.parse_element()?,
            application_reference: self.parse_element()?,
        })
    }

    /// Core segment parsing logic
    fn parse_segment(&mut self) -> Result<EdifactSegment, EdiError> {
        let tag = self.parse_segment_tag()?;
        let mut elements = Vec::new();

        while self.peek() != Some(self.delimiters.segment_terminator) {
            elements.push(self.parse_element()?);
            if self.peek() == Some(self.delimiters.data_separator) {
                self.consume_char()?;
            }
        }
        self.consume_segment_terminator()?;

        Ok(EdifactSegment { tag, elements })
    }

    /// Element parsing with component separation
    fn parse_element(&mut self) -> Result<EdifactElement, EdiError> {
        let mut components = Vec::new();
        let mut current = String::new();
        let mut in_escape = false;

        loop {
            match self.chars.peek() {
                Some(&c) if c == self.delimiters.component_separator && !in_escape => {
                    components.push(current);
                    current = String::new();
                    self.chars.next();
                    self.position += 1;
                }
                Some(&c) if c == self.delimiters.escape_character => {
                    in_escape = !in_escape;
                    self.chars.next();
                    self.position += 1;
                }
                Some(&c) if c == self.delimiters.data_separator => break,
                Some(&c) if c == self.delimiters.segment_terminator => break,
                Some(c) => {
                    current.push(*c);
                    self.chars.next();
                    self.position += 1;
                    in_escape = false;
                }
                None => break,
            }
        }

        components.push(current);
        Ok(EdifactElement { components })
    }

    /// Service string advice parsing (UNB+...)
    fn parse_service_string_advice(&mut self) -> Result<(), EdiError> {
        let re = Regex::new(r"^UNOA\d").unwrap();
        let mut advice = String::new();
        
        for _ in 0..5 {
            advice.push(*self.chars.peek().ok_or(EdiError::InvalidServiceStringAdvice)?);
            self.chars.next();
            self.position += 1;
        }

        if !re.is_match(&advice) {
            return Err(EdiError::InvalidServiceStringAdvice);
        }

        self.delimiters = EdiDelimiters {
            component_separator: advice.chars().nth(3).unwrap(),
            data_separator: advice.chars().nth(4).unwrap(),
            decimal_separator: '.',
            escape_character: '?',
            segment_terminator: advice.chars().nth(5).unwrap(),
        };

        Ok(())
    }

    /// Full validation of interchange structure
    fn validate_interchange(
        &self,
        unb: &UnbSegment,
        unz: &UnzSegment,
        message_count: usize,
    ) -> Result<(), EdiError> {
        if unb.control_reference != unz.interchange_control_reference {
            return Err(EdiError::ValidationError(
                "Control reference mismatch between UNB and UNZ".into(),
            ));
        }

        if unz.interchange_control_count as usize != message_count {
            return Err(EdiError::ValidationError(format!(
                "Message count mismatch: UNZ reports {}, actual {}",
                unz.interchange_control_count, message_count
            )));
        }

        Ok(())
    }

    // Additional helper methods...
}

#[cfg(test)]
mod tests {
    use super::*;
    use tracing_test::traced_test;

    const SAMPLE_EDIFACT: &str = "UNB+UNOA:1+SenderID+RecipientID+230516:1345+123456++1234'UNH+1+ORDERS:D:01B:UN'...";

    #[test]
    #[traced_test]
    fn test_full_parse() {
        let config = ParserConfig {
            allowed_versions: vec!["01B".into()],
            ..Default::default()
        };
        
        let mut parser = EdiParser::new(SAMPLE_EDIFACT, config).unwrap();
        let interchange = parser.parse_interchange().unwrap();
        
        assert_eq!(interchange.unb.sender_identification, "SenderID");
        assert_eq!(interchange.messages.len(), 1);
    }
}
